package sender

import (
	"encoding/binary"
	"fmt"
	"net"
	"testing"
)

var flowBenchmarkResult bool

// Compare in one process against the frozen original parser, including its
// original read lock. Synthetic desktop timings are not router throughput.
func BenchmarkFlowKeep(b *testing.B) {
	for _, n := range []int{1, 16, 128} {
		for _, shape := range []string{"udp4", "tcp4_miss", "tcp4_hit_last", "tcp6_ext_miss", "ipv4_fragment"} {
			b.Run(fmt.Sprintf("%s/%d", shape, n), func(b *testing.B) {
				s := &flowSet{}
				src, dst := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
				if shape == "tcp6_ext_miss" {
					src, dst = net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2")
				}
				for i := 0; i < n; i++ {
					s.track(&net.TCPAddr{IP: src, Port: 40000 + i}, &net.TCPAddr{IP: dst, Port: 18880})
				}
				frame := tcpFrame(src, dst, 49999, 18880, false)
				switch shape {
				case "udp4":
					frame[23] = 17
				case "tcp4_hit_last":
					binary.BigEndian.PutUint16(frame[34:36], uint16(40000+n-1))
				case "tcp6_ext_miss":
					frame = flowIPv6Frame(src, dst, 49999, 18880, 0, 43, 60, 51)
				case "ipv4_fragment":
					binary.BigEndian.PutUint16(frame[20:22], 1)
				}
				b.Run("indexed", func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						flowBenchmarkResult = s.keep(frame)
					}
				})
				b.Run("legacy", func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						s.mu.RLock()
						flowBenchmarkResult = legacyFlowKeep(s.items, frame)
						s.mu.RUnlock()
					}
				})
			})
		}
	}
}
