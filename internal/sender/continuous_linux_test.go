package sender

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radarsender/internal/radarupload"
)

func TestCaptureSurvivesServerOfflineAndRecovery(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && len(iface.HardwareAddr) == 6 {
			name = iface.Name
			break
		}
	}
	if name == "" {
		t.Fatal("integration test needs an Ethernet interface")
	}
	bin := t.TempDir()
	record, _ := radarupload.PCAPRecord(make([]byte, 60), time.Now())
	fixture, pids := filepath.Join(bin, "frame.pcap"), filepath.Join(bin, "capture-pids")
	if err = os.WriteFile(fixture, append(radarupload.PCAPHeader(), record...), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1\" = -i ] && [ \"$2\" = \"$RS_TEST_IFACE\" ] || exit 2\necho $$ >>\"$RS_TEST_PIDS\"\ntrap 'exit 0' INT TERM\nhead -c 24 \"$RS_TEST_PCAP\"\nwhile :; do tail -c +25 \"$RS_TEST_PCAP\"; sleep 0.02; done\n"
	ubus := "#!/bin/sh\nprintf '{\"up\":true,\"l3_device\":\"%s\"}' \"$RS_TEST_IFACE\"\n"
	for file, body := range map[string]string{"tcpdump": script, "ubus": ubus} {
		if err = os.WriteFile(filepath.Join(bin, file), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RS_TEST_PCAP", fixture)
	t.Setenv("RS_TEST_IFACE", name)
	t.Setenv("RS_TEST_PIDS", pids)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // No listener: first connection attempts really fail.
	var server *httptest.Server
	s := New(t.TempDir())
	s.retry = 50 * time.Millisecond
	if err = s.Configure(Config{Channel: "http://" + address + "#fixture"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
		if server != nil {
			server.CloseClientConnections()
			server.Close()
		}
	})
	await := func(condition func(Status) bool) Status {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			v := s.Snapshot()
			if condition(v) {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		v := s.Snapshot()
		t.Fatalf("state=%s capturing=%t captured=%d sent=%d offline=%d error=%s", v.State, v.Capturing, v.Captured, v.Packets, v.OfflineDropped, v.Error)
		return v
	}
	online := func() {
		t.Helper()
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/multi/channel/status" {
				_, _ = io.WriteString(w, `{"running":true}`)
				return
			}
			if r.URL.Path != "/multi/channel/ingest" {
				t.Error("unexpected route")
				return
			}
			header := make([]byte, 24)
			if _, err := io.ReadFull(r.Body, header); err != nil {
				return
			}
			var packets uint64
			h := make([]byte, 16)
			for {
				_, err := io.ReadFull(r.Body, h)
				if err == io.EOF {
					break
				}
				if err != nil {
					return
				}
				length := binary.LittleEndian.Uint32(h[8:12])
				if length > 65535 {
					t.Error("bad PCAP record")
					return
				}
				if _, err = io.CopyN(io.Discard, r.Body, int64(length)); err != nil {
					return
				}
				packets++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": packets})
		}))
		_ = srv.Listener.Close()
		srv.Listener, err = net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		srv.Start()
		server = srv
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	first := await(func(v Status) bool { return v.Capturing && v.State == "reconnecting" && v.OfflineDropped >= 3 })
	if first.Packets != 0 || first.Captured != first.OfflineDropped {
		t.Fatal("offline frames marked as uploaded")
	}
	online()
	sent := await(func(v Status) bool { return v.State == "sending" && v.Packets >= 3 })
	server.CloseClientConnections()
	server.Close()
	server = nil
	down := await(func(v Status) bool {
		return v.Capturing && v.State == "reconnecting" && v.OfflineDropped > sent.OfflineDropped+3
	})
	if down.Captured <= sent.Captured {
		t.Fatal("capture stopped with server")
	}
	online()
	await(func(v Status) bool { return v.State == "sending" && v.Packets >= down.Packets+3 })
	data, err := os.ReadFile(pids)
	if err != nil || len(strings.Fields(string(data))) != 1 {
		t.Fatal("capture process was restarted during network outage")
	}
	s.Stop()
	final := await(func(v Status) bool { return v.State == "idle" && !v.Capturing })
	if final.OfflineDropped == 0 || final.Packets == 0 || final.Errors == 0 {
		t.Fatal("continuous session lost counters")
	}
	if final.Captured != final.Packets+final.Dropped {
		t.Fatalf("packet accounting mismatch: %+v", final.Counters)
	}
}
