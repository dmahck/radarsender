package sender

import (
	"encoding/binary"
	"net"
	"sync"
)

type flow struct {
	src, dst net.IP
	sp, dp   uint16
}
type flowSet struct {
	mu    sync.RWMutex
	items []flow
}

func (s *flowSet) track(local, remote net.Addr) {
	l, ok := local.(*net.TCPAddr)
	r, rok := remote.(*net.TCPAddr)
	if !ok || !rok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 128 {
		copy(s.items, s.items[1:])
		s.items = s.items[:127]
	}
	s.items = append(s.items, flow{append(net.IP(nil), l.IP...), append(net.IP(nil), r.IP...), uint16(l.Port), uint16(r.Port)})
}
func (s *flowSet) keep(frame []byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.items {
		if f.matches(frame) || (flow{f.dst, f.src, f.dp, f.sp}).matches(frame) {
			return false
		}
	}
	return true
}
func (f flow) matches(b []byte) bool {
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
