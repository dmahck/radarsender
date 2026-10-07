package sender

import (
	"encoding/binary"
	"math/rand"
	"net"
	"sync"
	"testing"
)

func tcpFrame(src, dst net.IP, sp, dp uint16, vlan bool) []byte {
	header := 14
	if vlan {
		header = 18
	}
	b := make([]byte, header+40)
	if vlan {
		binary.BigEndian.PutUint16(b[12:], 0x8100)
		binary.BigEndian.PutUint16(b[16:], 0x800)
	} else {
		binary.BigEndian.PutUint16(b[12:], 0x800)
	}
	ip := b[header:]
	ip[0] = 0x45
	ip[9] = 6
	copy(ip[12:16], src.To4())
	copy(ip[16:20], dst.To4())
	binary.BigEndian.PutUint16(ip[20:], sp)
	binary.BigEndian.PutUint16(ip[22:], dp)
	return b
}
func TestOwnUploadExcludedBothWaysIncludingVLAN(t *testing.T) {
	src, dst := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	s := &flowSet{}
	s.track(&net.TCPAddr{IP: src, Port: 41000}, &net.TCPAddr{IP: dst, Port: 18880})
	for _, vlan := range []bool{false, true} {
		if s.keep(tcpFrame(src, dst, 41000, 18880, vlan)) || s.keep(tcpFrame(dst, src, 18880, 41000, vlan)) {
			t.Fatal("upload traffic feeds back into capture")
		}
		if !s.keep(tcpFrame(src, dst, 41001, 18880, vlan)) {
			t.Fatal("unrelated connection removed")
		}
	}
	for n := 0; n < 60; n++ {
		s.keep(make([]byte, n))
	}
}

func TestContinuousSessionKeepsFlowTrackingBounded(t *testing.T) {
	s := &flowSet{}
	src, dst := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	for p := 40000; p < 45000; p++ {
		s.track(&net.TCPAddr{IP: src, Port: p}, &net.TCPAddr{IP: dst, Port: 18880})
	}
	if len(s.items) != 128 || s.keep(tcpFrame(src, dst, 44999, 18880, false)) {
		t.Fatal("flow tracking leaked or lost active connection")
	}
	if len(s.tuples) > 256 || len(s.ipPairs) > 256 {
		t.Fatal("flow indexes grew beyond bounded tracking history")
	}
}

// Frozen pre-optimization matcher: differential checks must not share the new
// parser or its canonical-address/index implementation.
func legacyFlowMatches(f flow, b []byte) bool {
	if len(b) < 14 {
		return false
	}
	kind, pos := binary.BigEndian.Uint16(b[12:14]), 14
	for kind == 0x8100 || kind == 0x88a8 {
		if len(b) < pos+4 {
			return false
		}
		kind = binary.BigEndian.Uint16(b[pos+2 : pos+4])
		pos += 4
	}
	ip := b[pos:]
	var tcp []byte
	switch kind {
	case 0x800:
		if len(ip) < 20 || ip[0]>>4 != 4 || ip[9] != 6 || !net.IP(ip[12:16]).Equal(f.src) || !net.IP(ip[16:20]).Equal(f.dst) {
			return false
		}
		if binary.BigEndian.Uint16(ip[6:8])&0x1fff != 0 {
			return true
		}
		n := int(ip[0]&15) * 4
		if n < 20 || len(ip) < n+4 {
			return false
		}
		tcp = ip[n:]
	case 0x86dd:
		if len(ip) < 40 || ip[0]>>4 != 6 || !net.IP(ip[8:24]).Equal(f.src) || !net.IP(ip[24:40]).Equal(f.dst) {
			return false
		}
		next, n := ip[6], 40
		for next != 6 {
			if next == 44 {
				return len(ip) >= n+8 && ip[n] == 6
			}
			if (next != 0 && next != 43 && next != 60 && next != 51) || len(ip) < n+2 {
				return false
			}
			size := (int(ip[n+1]) + 1) * 8
			if next == 51 {
				size = (int(ip[n+1]) + 2) * 4
			}
			next, n = ip[n], n+size
			if n > len(ip) {
				return false
			}
		}
		tcp = ip[n:]
	default:
		return false
	}
	return len(tcp) >= 4 && binary.BigEndian.Uint16(tcp[:2]) == f.sp && binary.BigEndian.Uint16(tcp[2:4]) == f.dp
}

func legacyFlowKeep(items []flow, frame []byte) bool {
	for _, f := range items {
		if legacyFlowMatches(f, frame) || legacyFlowMatches(flow{f.dst, f.src, f.dp, f.sp}, frame) {
			return false
		}
	}
	return true
}

func flowIPv6Frame(src, dst net.IP, sp, dp uint16, extensions ...byte) []byte {
	b := make([]byte, 14+40+8*len(extensions)+20)
	binary.BigEndian.PutUint16(b[12:14], 0x86dd)
	ip := b[14:]
	ip[0], ip[6] = 0x60, 6
	copy(ip[8:24], src.To16())
	copy(ip[24:40], dst.To16())
	if len(extensions) != 0 {
		ip[6] = extensions[0]
	}
	for i := range extensions {
		n := 40 + 8*i
		ip[n] = 6
		if i+1 < len(extensions) {
			ip[n] = extensions[i+1]
		}
	}
	n := 40 + 8*len(extensions)
	binary.BigEndian.PutUint16(ip[n:n+2], sp)
	binary.BigEndian.PutUint16(ip[n+2:n+4], dp)
	return b
}

func flowWithVLAN(frame []byte, tags ...uint16) []byte {
	b := make([]byte, len(frame)+4*len(tags))
	copy(b[:12], frame[:12])
	for i, tag := range tags {
		binary.BigEndian.PutUint16(b[12+4*i:14+4*i], tag)
	}
	copy(b[12+4*len(tags):], frame[12:])
	return b
}

func flowDifferentialCorpus() [][]byte {
	v4a, v4b := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	v6a, v6b := net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2")
	var corpus [][]byte
	for _, reverse := range []bool{false, true} {
		src4, dst4, src6, dst6, sp, dp := v4a, v4b, v6a, v6b, uint16(41000), uint16(18880)
		if reverse {
			src4, dst4, src6, dst6, sp, dp = dst4, src4, dst6, src6, dp, sp
		}
		v4 := tcpFrame(src4, dst4, sp, dp, false)
		corpus = append(corpus, v4, flowWithVLAN(v4, 0x8100), flowWithVLAN(v4, 0x88a8, 0x8100))
		for _, flags := range []uint16{1, 0x2000, 0x2001, 0x1fff} {
			b := append([]byte(nil), v4...)
			binary.BigEndian.PutUint16(b[20:22], flags)
			corpus = append(corpus, b)
			b = append([]byte(nil), b...)
			binary.BigEndian.PutUint16(b[34:36], 49999)
			corpus = append(corpus, b)
		}
		options := append([]byte(nil), v4[:34]...)
		options = append(options, 0, 0, 0, 0)
		options = append(options, v4[34:]...)
		options[14] = 0x46
		corpus = append(corpus, options)
		for _, ext := range [][]byte{nil, {0}, {43}, {60}, {51}, {0, 43, 60, 51}, {44}, {0, 44}, {44, 60}, {50}, {17}} {
			b := flowIPv6Frame(src6, dst6, sp, dp, ext...)
			corpus = append(corpus, b, flowWithVLAN(b, 0x88a8, 0x8100))
			b = append([]byte(nil), b...)
			binary.BigEndian.PutUint16(b[len(b)-20:len(b)-18], 49999)
			corpus = append(corpus, b)
		}
		// net.IP.Equal also treats IPv4-mapped IPv6 addresses as IPv4.
		corpus = append(corpus, flowIPv6Frame(src4, dst4, sp, dp), flowIPv6Frame(src4, dst4, sp, dp, 44))
	}
	return corpus
}

func TestFlowFilterMatchesLegacyCorpus(t *testing.T) {
	s := &flowSet{}
	for _, pair := range [][2]string{{"192.0.2.1", "192.0.2.2"}, {"2001:db8::1", "2001:db8::2"}} {
		s.track(&net.TCPAddr{IP: net.ParseIP(pair[0]), Port: 41000}, &net.TCPAddr{IP: net.ParseIP(pair[1]), Port: 18880})
	}
	s.track(&net.TCPAddr{IP: net.IP{1, 2, 3}, Port: 41000}, &net.TCPAddr{Port: 18880})
	sets := []*flowSet{s, {}}
	for _, pair := range [][2]net.IP{
		{net.IP{192, 0, 2, 1}, net.ParseIP("192.0.2.2")},
		{net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2")},
		{net.IP{1, 2, 3}, nil},
	} {
		single := &flowSet{}
		single.track(&net.TCPAddr{IP: pair[0], Port: 41000}, &net.TCPAddr{IP: pair[1], Port: 18880})
		sets = append(sets, single)
	}
	checked := 0
	check := func(frame []byte) {
		t.Helper()
		for i, set := range sets {
			want := legacyFlowKeep(set.items, frame)
			if got := set.keep(frame); got != want {
				t.Fatalf("set %d frame %d %x: keep=%v legacy=%v", i, checked, frame, got, want)
			}
			checked++
		}
	}
	rng := rand.New(rand.NewSource(1))
	for _, frame := range flowDifferentialCorpus() {
		check(frame)
		for n := 0; n <= len(frame); n++ {
			check(frame[:n])
		}
		for n := 0; n < 200; n++ {
			b := append([]byte(nil), frame...)
			for m := 0; m < 1+n%4; m++ {
				b[rng.Intn(len(b))] = byte(rng.Intn(256))
			}
			check(b)
		}
	}
	for n := 0; n < 10000; n++ {
		b := make([]byte, rng.Intn(256))
		_, _ = rng.Read(b)
		check(b)
	}
	t.Logf("%d malformed/truncated/directional/VLAN/fragment/extension cases match frozen legacy oracle", checked)
}

func TestFlowIndexDuplicateEvictionMatchesLegacy(t *testing.T) {
	src, dst := net.IP{192, 0, 2, 1}, net.ParseIP("192.0.2.2")
	s := &flowSet{}
	for n := 0; n < 600; n++ {
		p := 41000 + n%150
		if n%3 == 0 {
			p = 41000
		}
		s.track(&net.TCPAddr{IP: src, Port: p}, &net.TCPAddr{IP: dst, Port: 18880})
		if len(s.items) > 128 || len(s.tuples) > 256 || len(s.ipPairs) > 256 {
			t.Fatal("unbounded index")
		}
		for p := 41000; p < 41150; p++ {
			for _, b := range [][]byte{tcpFrame(src, dst, uint16(p), 18880, false), tcpFrame(dst, src, 18880, uint16(p), true)} {
				if got, want := s.keep(b), legacyFlowKeep(s.items, b); got != want {
					t.Fatalf("track %d port %d: keep=%v legacy=%v", n, p, got, want)
				}
			}
		}
		for _, count := range s.tuples {
			if count <= 0 {
				t.Fatal("nonpositive tuple count")
			}
		}
		for _, count := range s.ipPairs {
			if count <= 0 {
				t.Fatal("nonpositive pair count")
			}
		}
	}
}

func TestFlowFilterConcurrentTrackAndKeep(t *testing.T) {
	s := &flowSet{}
	src, dst := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	b := tcpFrame(src, dst, 41000, 18880, false)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for n := 0; n < 2000; n++ {
			s.track(&net.TCPAddr{IP: src, Port: 41000 + n}, &net.TCPAddr{IP: dst, Port: 18880})
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 10000; n++ {
			s.keep(b)
		}
	}()
	wg.Wait()
}
