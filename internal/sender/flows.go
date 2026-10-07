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

// Fixed-size, IPv4-mapped addresses preserve net.IP.Equal semantics without
// allocating when a captured frame is parsed. Both directions are indexed.
type flowKey struct {
	src, dst [16]byte
	sp, dp   uint16
}
type flowIPPair struct{ src, dst [16]byte }

type flowSet struct {
	mu          sync.RWMutex
	items       []flow
	tuples      map[flowKey]int
	ipPairs     map[flowIPPair]int
	latest      flowKey
	latestValid bool
}

func (s *flowSet) track(local, remote net.Addr) {
	l, ok := local.(*net.TCPAddr)
	r, rok := remote.(*net.TCPAddr)
	if !ok || !rok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tuples == nil {
		s.tuples = make(map[flowKey]int)
		s.ipPairs = make(map[flowIPPair]int)
	}
	if len(s.items) == 128 {
		s.index(s.items[0], -1)
		copy(s.items, s.items[1:])
		s.items = s.items[:127]
	}
	f := flow{append(net.IP(nil), l.IP...), append(net.IP(nil), r.IP...), uint16(l.Port), uint16(r.Port)}
	s.items = append(s.items, f)
	s.latest, s.latestValid = s.index(f, 1)
}

func canonicalFlowIP(ip net.IP) (out [16]byte, ok bool) {
	switch len(ip) {
	case net.IPv4len:
		out[10], out[11] = 0xff, 0xff
		copy(out[12:], ip)
	case net.IPv6len:
		copy(out[:], ip)
	default:
		return out, false
	}
	return out, true
}

// Reference counts preserve duplicate tracking and exact oldest-first eviction.
// A tuple may be shared by several entries or by an entry's reverse direction.
func (s *flowSet) index(f flow, delta int) (flowKey, bool) {
	src, srcOK := canonicalFlowIP(f.src)
	dst, dstOK := canonicalFlowIP(f.dst)
	if !srcOK || !dstOK {
		return flowKey{}, false
	}
	key := flowKey{src, dst, f.sp, f.dp}
	for _, k := range [2]flowKey{key, {dst, src, f.dp, f.sp}} {
		if n := s.tuples[k] + delta; n == 0 {
			delete(s.tuples, k)
		} else {
			s.tuples[k] = n
		}
		pair := flowIPPair{k.src, k.dst}
		if n := s.ipPairs[pair] + delta; n == 0 {
			delete(s.ipPairs, pair)
		} else {
			s.ipPairs[pair] = n
		}
	}
	return key, true
}

func (s *flowSet) keep(frame []byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.items) == 0 {
		return true
	}
	k, ipOnly, ok := parseFlowFrame(frame)
	if !ok {
		return true
	}
	// The usual first connection needs no hash lookup. A history still uses the
	// bounded indexes rather than reparsing for each prior connection.
	if len(s.items) == 1 {
		if !s.latestValid {
			return true
		}
		f := s.latest
		if ipOnly {
			return !((k.src == f.src && k.dst == f.dst) || (k.src == f.dst && k.dst == f.src))
		}
		return !(k == f || (k.src == f.dst && k.dst == f.src && k.sp == f.dp && k.dp == f.sp))
	}
	if ipOnly {
		return s.ipPairs[flowIPPair{k.src, k.dst}] == 0
	}
	return s.tuples[k] == 0
}

// Parse just once per frame. Fragment decisions intentionally retain the
// existing IP-pair-only behavior: IPv4 noninitial TCP fragments, and IPv6
// fragment headers whose immediate next header is TCP, have no port lookup.
func parseFlowFrame(b []byte) (k flowKey, ipOnly, ok bool) {
	if len(b) < 14 {
		return k, false, false
	}
	kind, pos := binary.BigEndian.Uint16(b[12:14]), 14
	for kind == 0x8100 || kind == 0x88a8 {
		if len(b) < pos+4 {
			return k, false, false
		}
		kind = binary.BigEndian.Uint16(b[pos+2 : pos+4])
		pos += 4
	}
	ip := b[pos:]
	var tcp []byte
	switch kind {
	case 0x800:
		if len(ip) < 20 || ip[0]>>4 != 4 || ip[9] != 6 {
			return k, false, false
		}
		k.src, _ = canonicalFlowIP(net.IP(ip[12:16]))
		k.dst, _ = canonicalFlowIP(net.IP(ip[16:20]))
		if binary.BigEndian.Uint16(ip[6:8])&0x1fff != 0 {
			return k, true, true
		}
		n := int(ip[0]&15) * 4
		if n < 20 || len(ip) < n+4 {
			return k, false, false
		}
		tcp = ip[n:]
	case 0x86dd:
		if len(ip) < 40 || ip[0]>>4 != 6 {
			return k, false, false
		}
		copy(k.src[:], ip[8:24])
		copy(k.dst[:], ip[24:40])
		next, n := ip[6], 40
		for next != 6 {
			if next == 44 {
				return k, true, len(ip) >= n+8 && ip[n] == 6
			}
			if (next != 0 && next != 43 && next != 60 && next != 51) || len(ip) < n+2 {
				return k, false, false
			}
			size := (int(ip[n+1]) + 1) * 8
			if next == 51 {
				size = (int(ip[n+1]) + 2) * 4
			}
			next, n = ip[n], n+size
			if n > len(ip) {
				return k, false, false
			}
		}
		tcp = ip[n:]
	default:
		return k, false, false
	}
	if len(tcp) < 4 {
		return k, false, false
	}
	k.sp, k.dp = binary.BigEndian.Uint16(tcp[:2]), binary.BigEndian.Uint16(tcp[2:4])
	return k, false, true
}
