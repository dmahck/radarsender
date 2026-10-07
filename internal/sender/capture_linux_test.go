package sender

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"radarsender/internal/radarupload"
	"strings"
	"testing"
	"time"
)

func TestLinuxCaptureUploadAndGracefulStop(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	iface := ""
	for _, i := range interfaces {
		if i.Flags&net.FlagUp != 0 && len(i.HardwareAddr) == 6 {
			iface = i.Name
			break
		}
	}
	if iface == "" {
		t.Skip("no Ethernet interface in test environment")
	}
	bin := t.TempDir()
	fixture := filepath.Join(bin, "fixture.pcap")
	record, _ := radarupload.PCAPRecord(make([]byte, 60), time.Now())
	if err = os.WriteFile(fixture, append(radarupload.PCAPHeader(), record...), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1\" = -i ] && [ \"$2\" = \"$RS_TEST_IFACE\" ] || exit 2\ntrap 'exit 0' INT TERM\nhead -c 24 \"$RS_TEST_PCAP\"\nwhile [ ! -f \"$RS_TEST_UPLOAD\" ]; do sleep 0.01; done\ntail -c +25 \"$RS_TEST_PCAP\"\nwhile :; do sleep 1; done\n"
	if err = os.WriteFile(filepath.Join(bin, "tcpdump"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RS_TEST_PCAP", fixture)
	t.Setenv("RS_TEST_IFACE", iface)
	readyPath := filepath.Join(bin, "upload-ready")
	t.Setenv("RS_TEST_UPLOAD", readyPath)
	ubus := "#!/bin/sh\n[ \"$*\" = 'call network.interface.lan status' ] || exit 2\nprintf '{\"up\":true,\"l3_device\":\"%s\"}' \"$RS_TEST_IFACE\"\n"
	if err = os.WriteFile(filepath.Join(bin, "ubus"), []byte(ubus), 0700); err != nil {
		t.Fatal(err)
	}
	received := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/multi/channel/status":
			_, _ = io.WriteString(w, `{"running":true}`)
		case "/multi/channel/ingest":
			if err := os.WriteFile(readyPath, []byte("ready"), 0600); err != nil {
				t.Error(err)
			}
			data, _ := io.ReadAll(r.Body)
			received <- len(data)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": 1})
		default:
			t.Error("unexpected endpoint")
		}
	}))
	defer server.Close()
	s := New(t.TempDir())
	c := Config{Channel: server.URL + "#fixture"}
	if err = s.Configure(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, "sending")
	until := time.Now().Add(3 * time.Second)
	for s.Snapshot().Packets != 1 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	s.Stop()
	v := waitState(t, s, "idle")
	if v.Packets != 1 || v.Errors != 0 || v.Interface != iface {
		t.Fatalf("bad final stats %+v", v.Counters)
	}
	if n := <-received; n != 100 {
		t.Fatalf("PCAP size=%d", n)
	}
}
func TestActualTcpdumpOfflinePCAP(t *testing.T) {
	tcpdump, err := exec.LookPath("tcpdump")
	if err != nil {
		t.Skip("tcpdump unavailable")
	}
	path := filepath.Join(t.TempDir(), "sample.pcap")
	record, _ := radarupload.PCAPRecord(make([]byte, 60), time.Now())
	if err = os.WriteFile(path, append(radarupload.PCAPHeader(), record...), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := exec.Command(tcpdump, "-nn", "-r", path, "-w", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = readPCAP(strings.NewReader(string(data)), func() {}, func([]byte, time.Time) { count++ })
	if err != nil || count != 1 {
		t.Fatal("real tcpdump output incompatible", err)
	}
}
