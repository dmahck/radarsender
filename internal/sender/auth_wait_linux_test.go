package sender

import (
	"bytes"
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
	"sync/atomic"
	"testing"
	"time"

	"radarsender/internal/radarupload"
)

// Real HTTP and a continuous PCAP-producing child validate the authoritative
// server packet surface, not just the intermediate waiting/sending status.
func TestAuthorizationWaitKeepsCaptureAndRecoversWithUpdatedBearer(t *testing.T) {
	for _, failure := range []struct {
		name        string
		code        int
		onIngest    bool
		oldRequests uint64
	}{
		{"status-401", http.StatusUnauthorized, false, 1},
		{"status-403", http.StatusForbidden, false, 1},
		{"ingest-401-after-status-success", http.StatusUnauthorized, true, 2},
	} {
		t.Run(failure.name, func(t *testing.T) {
			interfaces, err := net.Interfaces()
			if err != nil {
				t.Fatal(err)
			}
			iface := ""
			for _, candidate := range interfaces {
				if candidate.Flags&net.FlagUp != 0 && len(candidate.HardwareAddr) == 6 {
					iface = candidate.Name
					break
				}
			}
			if iface == "" {
				t.Fatal("integration test needs an Ethernet interface")
			}
			bin := t.TempDir()
			oldPCAP, newPCAP := filepath.Join(bin, "offline.pcap"), filepath.Join(bin, "updated.pcap")
			pids, switched := filepath.Join(bin, "capture-pids"), filepath.Join(bin, "capture-switched")
			phase := filepath.Join(bin, "use-updated-frame")
			frame := func(marker byte) []byte {
				b := bytes.Repeat([]byte{marker}, 60)
				binary.BigEndian.PutUint16(b[12:14], 0x88b5)
				return b
			}
			freshFrame := frame(0xb2)
			for path, data := range map[string][]byte{oldPCAP: frame(0xa1), newPCAP: freshFrame} {
				record, recordErr := radarupload.PCAPRecord(data, time.Unix(1700000000, 123456000))
				if recordErr != nil {
					t.Fatal(recordErr)
				}
				if err = os.WriteFile(path, append(radarupload.PCAPHeader(), record...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/sh
[ "$1" = -i ] && [ "$2" = "$RS_AUTH_TEST_IFACE" ] || exit 2
echo $$ >>"$RS_AUTH_TEST_PIDS"
trap 'exit 0' INT TERM
head -c 24 "$RS_AUTH_TEST_OLD_PCAP"
while :; do
    if [ -f "$RS_AUTH_TEST_PHASE" ]; then
        tail -c +25 "$RS_AUTH_TEST_NEW_PCAP"
        : >"$RS_AUTH_TEST_SWITCHED"
    else
        tail -c +25 "$RS_AUTH_TEST_OLD_PCAP"
    fi
    sleep 0.02
done
`
			if err = os.WriteFile(filepath.Join(bin, "tcpdump"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			for key, value := range map[string]string{
				"RS_AUTH_TEST_IFACE": iface, "RS_AUTH_TEST_PIDS": pids,
				"RS_AUTH_TEST_OLD_PCAP": oldPCAP, "RS_AUTH_TEST_NEW_PCAP": newPCAP,
				"RS_AUTH_TEST_PHASE": phase, "RS_AUTH_TEST_SWITCHED": switched,
			} {
				t.Setenv(key, value)
			}
			var staleRequests, freshStatus, freshIngest, received atomic.Uint64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "" || r.URL.Fragment != "" {
					t.Error("authentication material placed in the request URL")
				}
				if r.Header.Get("Authorization") == "Bearer fixture-stale" {
					staleRequests.Add(1)
					if failure.onIngest && r.URL.Path == "/multi/channel/status" {
						_, _ = io.WriteString(w, `{"running":true,"receiving":false}`)
						return
					}
					if failure.onIngest && r.URL.Path != "/multi/channel/ingest" {
						t.Error("unexpected stale authenticated route")
					}
					if failure.onIngest {
						// Match the gateway/client early-rejection fixture: do not
						// drain an endless chunked upload before sending its 401.
						w.Header().Set("Connection", "close")
					}
					w.WriteHeader(failure.code)
					if failure.onIngest {
						w.(http.Flusher).Flush()
					}
					return
				}
				if r.Header.Get("Authorization") != "Bearer fixture-updated" {
					t.Error("recovery did not use the newly configured bearer")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/multi/channel/status":
					freshStatus.Add(1)
					_, _ = io.WriteString(w, `{"running":true,"receiving":false}`)
				case "/multi/channel/ingest":
					freshIngest.Add(1)
					if r.Header.Get("X-Radar-Sender") == "" {
						t.Error("recovered ingest omitted its sender identity")
					}
					header := make([]byte, 24)
					if _, err := io.ReadFull(r.Body, header); err != nil {
						return
					}
					if !bytes.Equal(header, radarupload.PCAPHeader()) {
						t.Error("recovered stream changed the PCAP header")
						return
					}
					var packets uint64
					for {
						h := make([]byte, 16)
						_, err := io.ReadFull(r.Body, h)
						if err == io.EOF {
							break
						}
						if err != nil {
							return
						}
						if binary.LittleEndian.Uint32(h[8:12]) != 60 || binary.LittleEndian.Uint32(h[12:16]) != 60 {
							t.Error("recovered stream changed complete frame lengths")
							return
						}
						data := make([]byte, 60)
						if _, err = io.ReadFull(r.Body, data); err != nil {
							return
						}
						if !bytes.Equal(data, freshFrame) {
							t.Error("offline frames were replayed or the recovered frame was modified")
						}
						packets++
						received.Add(1)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": packets})
				default:
					t.Error("unexpected authenticated route")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			s := New(t.TempDir())
			s.detect = func(context.Context) (string, error) { return iface, nil }
			s.retry = 20 * time.Millisecond
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := s.Close(ctx); err != nil {
					t.Error(err)
				}
				server.CloseClientConnections()
				server.Close()
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
				t.Fatalf("state=%s capturing=%t captured=%d sent=%d offline=%d attempts=%d", v.State, v.Capturing, v.Captured, v.Packets, v.OfflineDropped, v.Attempts)
				return v
			}
			if err = s.Configure(Config{Channel: server.URL + "#fixture-stale"}); err != nil {
				t.Fatal(err)
			}
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			waiting := await(func(v Status) bool { return v.State == "waiting_channel" && v.Capturing && v.Captured >= 3 })
			later := await(func(v Status) bool { return v.State == "waiting_channel" && v.Captured >= waiting.Captured+8 })
			if staleRequests.Load() != failure.oldRequests || later.Attempts != 1 || later.Errors != 1 || later.OfflineDropped <= waiting.OfflineDropped {
				t.Fatal("waiting retried rejected authentication, stopped capture, or retained offline packets")
			}
			if !failure.onIngest && (later.Packets != 0 || later.Captured != later.OfflineDropped) {
				t.Fatal("pre-upload authorization failure treated offline packets as uploaded")
			}
			if err = os.WriteFile(phase, nil, 0600); err != nil {
				t.Fatal(err)
			}
			// Wait until the same child has switched its output and several new
			// frames were consumed while still offline; no old frame can race attach.
			await(func(v Status) bool {
				_, err := os.Stat(switched)
				return err == nil && v.Captured >= later.Captured+4
			})
			newChannel := server.URL + "#fixture-updated"
			if err = s.Configure(Config{Channel: newChannel}); err != nil {
				t.Fatal(err)
			}
			sent := await(func(v Status) bool {
				return v.State == "sending" && v.Packets >= later.Packets+3 && received.Load() >= 3
			})
			if !sent.Capturing || sent.Attempts != 2 || sent.OfflineDropped < later.OfflineDropped || staleRequests.Load() != failure.oldRequests || freshStatus.Load() != 1 || freshIngest.Load() != 1 {
				t.Fatal("changed bearer did not recover the same continuous capture session")
			}
			data, err := os.ReadFile(pids)
			if err != nil || len(strings.Fields(string(data))) != 1 {
				t.Fatal("capture child restarted while waiting for or updating the channel")
			}
			redacted, err := json.Marshal(s.Snapshot())
			if err != nil || bytes.Contains(redacted, []byte("fixture-stale")) || bytes.Contains(redacted, []byte("fixture-updated")) {
				t.Fatal("ordinary status exposed a channel credential")
			}
			s.Stop()
			final := await(func(v Status) bool { return v.State == "idle" && !v.Capturing })
			if final.Captured != final.Packets+final.Dropped || final.Packets != later.Packets+received.Load() || final.OfflineDropped == 0 || final.Errors != 1 || final.Attempts != 2 {
				t.Fatal("stop after channel recovery lost capture, upload, or offline accounting")
			}
			loaded := New(s.dir)
			if loaded.config.Channel != newChannel || loaded.Snapshot().State != "idle" || !loaded.Snapshot().HasChannel {
				t.Fatal("recovery did not persist the changed channel or restarted capture on daemon load")
			}
		})
	}
}
