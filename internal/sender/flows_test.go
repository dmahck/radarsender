package sender

import (
	"encoding/binary"
	"net"
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
}
