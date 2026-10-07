package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// This acceptance test uses the release tcpdump, a synthetic Ethernet frame,
// and a local HTTP receiver. Run in a disposable container with CAP_NET_RAW.
func TestBundledCaptureUploadsRealEthernet(t *testing.T) {
	binary := os.Getenv("RS_TEST_REAL_TCPDUMP")
	if binary == "" {
		t.Skip("set RS_TEST_REAL_TCPDUMP to a release binary in a disposable container")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("disposable Docker container required")
	}
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	selected, err := findTCPDump("/usr/lib/radarsender/tcpdump")
	if err != nil || selected != binary {
		t.Fatal("test must execute the specified release binary", selected, err)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var iface net.Interface
	for _, candidate := range interfaces {
		if candidate.Flags&net.FlagUp != 0 && len(candidate.HardwareAddr) == 6 {
			iface = candidate
			break
		}
	}
	if iface.Name == "" {
		t.Fatal("test requires a container Ethernet interface")
	}
	frame := make([]byte, 60)
	copy(frame[:6], iface.HardwareAddr)
	copy(frame[6:12], iface.HardwareAddr)
	frame[12], frame[13] = 0x88, 0xb5 // IEEE local experimental EtherType.
	copy(frame[14:], "RadarSender bundled capture acceptance")
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/multi/channel/status" {
			_, _ = io.WriteString(w, `{"running":true}`)
			return
		}
		if r.URL.Path != "/multi/channel/ingest" {
			t.Error("unexpected route")
			return
		}
		var packets uint64
		err := readPCAP(r.Body, func() {}, func(data []byte, _ time.Time) {
			packets++
			if bytes.Equal(frame, data) {
				select {
				case received <- struct{}{}:
				default:
				}
			}
		})
		if err != nil {
			t.Error("receiver got invalid PCAP", err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": packets})
	}))
	defer server.Close()
	s := New(t.TempDir())
	s.detect = func(context.Context) (string, error) { return iface.Name, nil }
	if err = s.Configure(Config{Channel: server.URL + "#fixture"}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, "sending")
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, 0xb588)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err = syscall.Sendto(fd, frame, 0, &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: 0xb588}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("release tcpdump did not deliver the synthetic Ethernet frame")
	}
	s.Stop()
	v := waitState(t, s, "idle")
	if v.Packets == 0 || v.Errors != 0 || v.Capturing {
		t.Fatalf("unexpected final status: %+v", v.Counters)
	}
}
