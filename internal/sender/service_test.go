package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"radarsender/internal/radarupload"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func configured(t *testing.T) *Service {
	t.Helper()
	s := New(t.TempDir())
	s.detect = func(context.Context) (string, error) { return "br-lan", nil }
	c := Defaults()
	c.Channel = "http://192.0.2.1:18880#test-private-card"
	if err := s.Configure(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func waitState(t *testing.T, s *Service, state string) Status {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		v := s.Snapshot()
		if v.State == state {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state=%s; want %s", s.Snapshot().State, state)
	return Status{}
}
func TestBrokenConfigKeepsManagementUsable(t *testing.T) {
	for _, data := range []string{"{broken", `{"channel":12}`, `{"channel":"not-a-channel"}`, strings.Repeat("x", 8193)} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		s := New(dir)
		if !s.Snapshot().OK || s.Snapshot().ConfigError == "" {
			t.Fatal("invalid configuration did not remain manageable")
		}
		if s.Start() == nil {
			t.Fatal("invalid config started")
		}
		if err := s.Configure(Defaults()); err != nil {
			t.Fatal(err)
		}
		if s.Snapshot().ConfigError != "" {
			t.Fatal("configuration repair did not clear error")
		}
	}
}
func TestConfigPrivateAndNeverReturned(t *testing.T) {
	s := configured(t)
	data, _ := json.Marshal(s.Snapshot())
	if bytes.Contains(data, []byte("test-private-card")) {
		t.Fatal("credential exposed")
	}
	c := Defaults()
	if err := s.Configure(c); err != nil {
		t.Fatal(err)
	}
	loaded := New(s.dir)
	if !loaded.Snapshot().HasChannel || loaded.Snapshot().State != "idle" {
		t.Fatal("persistence or disabled-on-restart failed")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(s.dir, "config.json"))
		if info.Mode().Perm() != 0600 {
			t.Fatal("config is not private")
		}
	}
}

func TestLegacyRateAndInterfaceAreIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"channel":"http://192.0.2.1#fixture","interface":"wan","max_rate_kbps":1}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	if s.Snapshot().ConfigError != "" || !s.Snapshot().HasChannel || s.config.Interface != "" {
		t.Fatal("legacy configuration was not migrated")
	}
	if err := s.Configure(Defaults()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(data, &fields) != nil || len(fields) != 1 || fields["channel"] == nil {
		t.Fatal("obsolete settings were persisted")
	}
}

func TestUnavailableLANDoesNotStartCapture(t *testing.T) {
	s := configured(t)
	s.retry = 30 * time.Second
	s.detect = func(context.Context) (string, error) { return "", errLANUnavailable }
	var called atomic.Bool
	s.attempt = func(context.Context, Config, string, bool, func(), func(Counters)) (Counters, error) {
		called.Store(true)
		return Counters{}, nil
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	v := waitState(t, s, "reconnecting")
	if called.Load() || v.Interface != "" || v.Error != errLANUnavailable.Error() {
		t.Fatal("capture began without LAN")
	}
	s.Stop()
	waitState(t, s, "idle")
}

func TestLANIsRedetectedOnReconnect(t *testing.T) {
	s := configured(t)
	s.retry = time.Millisecond
	var detections atomic.Int32
	s.detect = func(context.Context) (string, error) {
		if detections.Add(1) == 1 {
			return "br-old", nil
		}
		return "br-new", nil
	}
	s.attempt = func(ctx context.Context, c Config, _ string, retry bool, ready func(), _ func(Counters)) (Counters, error) {
		if !retry {
			if c.Interface != "br-old" {
				t.Error("wrong first LAN")
			}
			return Counters{}, radarupload.ErrNetwork
		}
		if c.Interface != "br-new" {
			t.Error("stale LAN reused")
		}
		ready()
		<-ctx.Done()
		return Counters{}, nil
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if v := waitState(t, s, "sending"); v.Interface != "br-new" || detections.Load() != 2 {
		t.Fatal("detected interface not reflected in status")
	}
}
func TestNetworkRetryPreservesIdentityAndCounters(t *testing.T) {
	s := configured(t)
	s.retry = time.Millisecond
	var calls atomic.Int32
	var previous string
	s.attempt = func(ctx context.Context, c Config, id string, retry bool, ready func(), report func(Counters)) (Counters, error) {
		n := calls.Add(1)
		if n == 1 {
			previous = id
			if retry {
				t.Error("first connection treated as retry")
			}
			return Counters{Packets: 3}, radarupload.ErrNetwork
		}
		if id != previous || !retry {
			t.Error("identity not preserved")
		}
		report(Counters{Packets: 2})
		ready()
		<-ctx.Done()
		return Counters{Packets: 2}, nil
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	v := waitState(t, s, "sending")
	if v.Packets != 5 || v.Errors != 1 {
		t.Fatalf("bad retry counters %+v", v.Counters)
	}
	if s.Configure(Defaults()) == nil || s.Start() == nil {
		t.Fatal("active session can be reconfigured/started twice")
	}
	s.Stop()
	v = waitState(t, s, "idle")
	if v.Packets != 5 || v.Attempts != 2 {
		t.Fatal("stop lost totals")
	}
}
func TestAuthorizationFailureWaitsForChangedChannelAndResumes(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{{"unauthorized", radarupload.ErrAuth}, {"forbidden", radarupload.ErrForbidden}} {
		t.Run(failure.name, func(t *testing.T) {
			s := configured(t)
			s.retry = time.Millisecond
			oldChannel := "http://192.0.2.1:18880#test-private-card"
			newChannel := "http://192.0.2.1:18880#fixture-updated-card"
			var calls atomic.Int32
			var firstID string
			resumed := make(chan struct{}, 1)
			s.attempt = func(ctx context.Context, c Config, id string, retry bool, ready func(), report func(Counters)) (Counters, error) {
				if calls.Add(1) == 1 {
					firstID = id
					if c.Channel != oldChannel || retry {
						t.Error("initial authentication attempt changed channel or allowed existing input")
					}
					return Counters{Packets: 3, Bytes: 180}, failure.err
				}
				if c.Channel != newChannel || retry || id == "" || id == firstID {
					t.Error("updated channel reused stale credentials, sender identity, or retry privileges")
				}
				report(Counters{Packets: 2, Bytes: 120})
				ready()
				resumed <- struct{}{}
				<-ctx.Done()
				return Counters{Packets: 2, Bytes: 120}, nil
			}
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			waiting := waitState(t, s, "waiting_channel")
			if waiting.Attempts != 1 || waiting.Errors != 1 || waiting.Packets != 3 || waiting.Error != failure.err.Error() || waiting.RetrySeconds != 0 {
				t.Fatal("authorization wait lost counters or scheduled retry")
			}
			s.mu.Lock()
			capture := s.capture
			s.mu.Unlock()
			if capture == nil || s.Start() == nil {
				t.Fatal("authorization wait discarded active session or allowed a duplicate start")
			}
			assertWaiting := func() {
				t.Helper()
				time.Sleep(25 * time.Millisecond) // More than twenty retry periods.
				v := s.Snapshot()
				if v.State != "waiting_channel" || calls.Load() != 1 || v.Attempts != 1 || v.Packets != 3 || v.Errors != 1 || v.Error != failure.err.Error() {
					t.Fatal("unchanged or rejected configuration woke the expired channel")
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.capture != capture || s.config.Channel != oldChannel {
					t.Fatal("authorization wait replaced capture or the persisted channel")
				}
			}
			assertWaiting()
			for _, c := range []Config{Defaults(), {Channel: oldChannel}} {
				if err := s.Configure(c); err != nil {
					t.Fatal(err)
				}
				assertWaiting()
			}
			path := filepath.Join(s.dir, "config.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Configure(Config{Channel: "invalid-fixture-channel"}); err == nil {
				t.Fatal("invalid channel accepted while waiting")
			}
			assertWaiting()
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid channel changed persisted configuration")
			}
			// A directory at the atomic rename destination forces save failure on
			// every platform, including container root, without chmod assumptions.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := s.Configure(Config{Channel: newChannel}); err == nil {
				t.Fatal("failed persistence woke the new channel")
			}
			assertWaiting()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.Configure(Config{Channel: newChannel}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-resumed:
			case <-time.After(2 * time.Second):
				t.Fatal("persisted changed channel did not resume without another Start")
			}
			v := waitState(t, s, "sending")
			if v.Packets != 5 || v.Bytes != 300 || v.Errors != 1 || v.Attempts != 2 || calls.Load() != 2 {
				t.Fatal("resuming with a changed channel reset session counters")
			}
			s.mu.Lock()
			if s.capture != capture {
				t.Error("resuming changed the capture session")
			}
			s.mu.Unlock()
			loaded := New(s.dir)
			if loaded.config.Channel != newChannel || loaded.Snapshot().State != "idle" {
				t.Fatal("changed channel was not durably saved or autostarted after restart")
			}
			s.Stop()
			v = waitState(t, s, "idle")
			if v.Packets != 5 || v.Bytes != 300 || v.Attempts != 2 || v.Errors != 1 {
				t.Fatal("stop after authorization recovery lost session totals")
			}
		})
	}
}

func TestAuthorizationWaitStopAndCloseArePrompt(t *testing.T) {
	for _, action := range []string{"stop", "close"} {
		t.Run(action, func(t *testing.T) {
			s := configured(t)
			s.retry = 30 * time.Second
			s.attempt = func(context.Context, Config, string, bool, func(), func(Counters)) (Counters, error) {
				return Counters{}, radarupload.ErrAuth
			}
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			waitState(t, s, "waiting_channel")
			if action == "stop" {
				s.Stop()
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.Close(ctx); err != nil {
					t.Fatal("close did not interrupt channel wait:", err)
				}
			}
			v := waitState(t, s, "idle")
			if v.Attempts != 1 || v.Errors != 1 || v.Capturing {
				t.Fatal("channel-wait cancellation lost totals or left capture active")
			}
		})
	}
}

func TestCaptureFailureWhileWaitingForChannelIsTerminal(t *testing.T) {
	s := configured(t)
	failed := make(chan struct{})
	s.attempt = func(context.Context, Config, string, bool, func(), func(Counters)) (Counters, error) {
		s.capture.mu.Lock()
		s.capture.done = failed
		s.capture.mu.Unlock()
		return Counters{}, radarupload.ErrAuth
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, "waiting_channel")
	s.capture.mu.Lock()
	s.capture.err = radarupload.ErrCapture
	s.capture.mu.Unlock()
	close(failed)
	v := waitState(t, s, "error")
	if v.Error != radarupload.ErrCapture.Error() || v.Attempts != 1 || v.Errors != 1 || v.Capturing {
		t.Fatal("capture failure was hidden by authentication wait or retried")
	}
}
func TestStopInterruptsBackoff(t *testing.T) {
	s := configured(t)
	s.retry = 30 * time.Second
	s.attempt = func(context.Context, Config, string, bool, func(), func(Counters)) (Counters, error) {
		return Counters{}, radarupload.ErrNetwork
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, "reconnecting")
	s.Stop()
	waitState(t, s, "idle")
}
func TestStopPreservesFailedFinalAcknowledgment(t *testing.T) {
	s := configured(t)
	s.attempt = func(ctx context.Context, _ Config, _ string, _ bool, ready func(), _ func(Counters)) (Counters, error) {
		ready()
		<-ctx.Done()
		return Counters{Packets: 1, Errors: 1}, radarupload.ErrProtocol
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, "sending")
	s.Stop()
	v := waitState(t, s, "error")
	if v.Error != radarupload.ErrProtocol.Error() {
		t.Fatal("final acknowledgment error hidden")
	}
}
func TestManagementRejectsMalformedAndOversizeRequests(t *testing.T) {
	s := configured(t)
	for _, body := range []string{"{", strings.Repeat("x", 8193), `{"method":"status"} {}`} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid request code=%d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"status"}`)))
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-private-card") {
		t.Fatal("status failed or exposed credential")
	}
}

func TestChannelRPCIntentionallyReturnsOnlyChannelWithoutCaching(t *testing.T) {
	s := configured(t)
	if _, ok := Methods["channel"]; !ok {
		t.Fatal("privileged channel method not registered")
	}
	for _, method := range []string{"channel", "status"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"`+method+`"}`)))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("management response permits caching or has wrong content type")
		}
		if method == "status" {
			if bytes.Contains(w.Body.Bytes(), []byte("test-private-card")) {
				t.Fatal("status exposed the private channel")
			}
			continue
		}
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response) != 2 || response["ok"] != true || response["channel"] != "http://192.0.2.1:18880#test-private-card" {
			t.Fatal("channel RPC returned extra private state or did not return the configured channel")
		}
	}
}
