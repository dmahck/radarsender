package radarupload

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE"

func fixtureChannel(base string) string { return base + "/multi/channel/ingest#" + testToken }
func requireSession(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		t.Error("missing channel bearer")
	}
	if r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		t.Error("unexpected session credential or token in URL")
	}
}

func connectFixture(t *testing.T, ingest http.HandlerFunc) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/multi/channel/status":
			requireSession(t, r)
			io.WriteString(w, `{"running":true,"receiving":false}`)
		case "/multi/channel/ingest":
			requireSession(t, r)
			ingest(w, r)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	client, err := Connect(context.Background(), fixtureChannel(server.URL), Options{})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return client, func() { client.Close(); server.Close() }
}

func TestIdleUploadDetectsClosedConnection(t *testing.T) {
	for _, protocol := range []string{"http", "https"} {
		t.Run(protocol, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requireSession(t, r)
				if _, err := io.ReadFull(r.Body, make([]byte, 24)); err != nil {
					t.Error("missing PCAP header")
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			})
			client, cleanup := connectFixture(t, handler)
			defer cleanup()
			if protocol == "https" {
				server := httptest.NewTLSServer(handler)
				defer server.Close()
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				client.transport.TLSClientConfig = &tls.Config{RootCAs: roots}
				client.base = server.URL
			}
			stream := client.Open(context.Background())
			defer stream.Abort()
			stream.Write(PCAPHeader())
			// No more packets arrive. A broken connection must still wake the sender.
			select {
			case <-stream.Done():
				if !errors.Is(stream.Err(), ErrNetwork) {
					t.Fatalf("closed connection: %v", stream.Err())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("idle upload did not detect closed connection")
			}
		})
	}
}

func TestChannelStartAndFinitePCAP(t *testing.T) {
	var mu sync.Mutex
	var routes []string
	var tcpConnections atomic.Int32
	record, _ := PCAPRecord([]byte{1, 2, 3, 4}, time.Unix(1700000000, 123000))
	wanted := append(PCAPHeader(), record...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		routes = append(routes, r.URL.Path)
		mu.Unlock()
		requireSession(t, r)
		switch r.URL.Path {
		case "/multi/channel/status":
			io.WriteString(w, `{"running":false,"receiving":false}`)
		case "/multi/channel/start":
			data, _ := io.ReadAll(r.Body)
			if string(data) != "{}" {
				t.Error("start must have no device IP")
			}
			io.WriteString(w, `{"ok":true}`)
		case "/multi/channel/ingest":
			if r.Header.Get("Content-Type") != "application/vnd.tcpdump.pcap" {
				t.Error("wrong content type")
			}
			data, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(data, wanted) {
				t.Error("PCAP stream changed")
			}
			io.WriteString(w, `{"ok":true,"packets":1}`)
		default:
			t.Error("unexpected route")
		}
	}))
	defer server.Close()
	client, err := Connect(context.Background(), fixtureChannel(server.URL), Options{OnConnect: func(local, remote net.Addr) {
		if local == nil || remote == nil {
			t.Error("missing socket tuple")
		}
		tcpConnections.Add(1)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.http.Timeout != 0 || client.transport.ResponseHeaderTimeout != 0 {
		t.Fatal("upload has whole-session timeout")
	}
	stream := client.Open(context.Background())
	defer stream.Abort()
	for _, chunk := range [][]byte{wanted[:7], wanted[7:24], wanted[24:]} {
		if _, err = stream.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	result, err := stream.Close()
	if err != nil || result.Packets != 1 {
		t.Fatalf("EOF not confirmed: %+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(routes, []string{"/multi/channel/status", "/multi/channel/start", "/multi/channel/ingest"}) {
		t.Fatalf("routes: %v", routes)
	}
	if tcpConnections.Load() < 1 {
		t.Fatal("connections not tracked")
	}
}

func TestAuthenticationStopsWithoutRetries(t *testing.T) {
	for _, tc := range []struct {
		name, route string
		code        int
		body        string
		want        error
	}{
		{"invalid-card", "/multi/channel/status", 401, "secret-card-must-not-echo", ErrAuth},
		{"login-full", "/multi/channel/status", 503, FullMessage, ErrFull},
		{"expired", "/multi/channel/status", 401, "expired", ErrAuth},
		{"start-full", "/multi/channel/start", 503, FullMessage, ErrFull},
		{"throttle", "/multi/channel/status", 429, "sensitive diagnostic", ErrRate},
		{"upstream-failure", "/multi/channel/status", 503, "provider private token", ErrServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var loginCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/multi/channel/status" {
					loginCount.Add(1)
				}
				if r.URL.Path == tc.route {
					http.Error(w, tc.body, tc.code)
					return
				}
				switch r.URL.Path {
				case "/multi/channel/status":
					io.WriteString(w, `{"running":false}`)
				default:
					t.Error("request after decisive rejection")
				}
			}))
			defer server.Close()
			_, err := Connect(context.Background(), fixtureChannel(server.URL), Options{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if loginCount.Load() != 1 {
				t.Fatal("authentication retried")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatal("remote text leaked")
			}
		})
	}
}

func TestExistingInputIsNotTakenOver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/multi/channel/status":
			io.WriteString(w, `{"running":true,"receiving":true}`)
		default:
			t.Fatal("existing input must not be stopped or overwritten")
		}
	}))
	defer server.Close()
	_, err := Connect(context.Background(), fixtureChannel(server.URL), Options{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v", err)
	}
}

func TestSenderIDIsStableForUploadAndNotUsedOnControl(t *testing.T) {
	id, err := NewSenderID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("invalid sender ID: %q", id)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/multi/channel/status":
			if r.Header.Get("X-Radar-Sender") != "" {
				t.Error("control request claimed an upload identity")
			}
			io.WriteString(w, `{"running":true,"receiving":false}`)
		case "/multi/channel/ingest":
			if got := r.Header.Get("X-Radar-Sender"); got != id {
				t.Errorf("upload sender ID = %q, want %q", got, id)
			}
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
			}
			io.WriteString(w, `{"ok":true,"packets":0}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := Connect(context.Background(), fixtureChannel(server.URL), Options{SenderID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream := client.Open(context.Background())
	if _, err := stream.Write(PCAPHeader()); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEarlyUploadRejectionsReachIdleAndActiveWriters(t *testing.T) {
	for _, code := range []int{401, 403, 409, 400, 503} {
		for _, active := range []bool{false, true} {
			name := http.StatusText(code)
			if active {
				name += "-writing"
			}
			t.Run(name, func(t *testing.T) {
				client, cleanup := connectFixture(t, func(w http.ResponseWriter, r *http.Request) {
					// Real gateway force-closes rejected streamed requests.
					w.Header().Set("Connection", "close")
					http.Error(w, FullMessage, code)
				})
				defer cleanup()
				stream := client.Open(context.Background())
				defer stream.Abort()
				written := make(chan error, 1)
				if active {
					go func() { _, err := stream.Write(make([]byte, 4<<20)); written <- err }()
				}
				select {
				case <-stream.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("early response did not interrupt")
				}
				want := responseError(code, []byte(FullMessage))
				if !errors.Is(stream.Err(), want) {
					t.Fatalf("got %v want %v", stream.Err(), want)
				}
				if active {
					select {
					case err := <-written:
						if !errors.Is(err, want) {
							t.Fatalf("writer got %v want %v", err, want)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("writer leaked")
					}
				}
			})
		}
	}
}

func TestFinalResponseAndCancellation(t *testing.T) {
	for _, body := range []string{`{"ok":false}`, `not-json`, `{"ok":true,"packets":"bad"}`} {
		t.Run(body, func(t *testing.T) {
			client, cleanup := connectFixture(t, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); io.WriteString(w, body) })
			defer cleanup()
			stream := client.Open(context.Background())
			defer stream.Abort()
			if _, err := stream.Write(PCAPHeader()); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Close(); !errors.Is(err, ErrProtocol) {
				t.Fatalf("unverified final response: %v", err)
			}
		})
	}
	t.Run("cancellation", func(t *testing.T) {
		client, cleanup := connectFixture(t, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) })
		defer cleanup()
		ctx, cancel := context.WithCancel(context.Background())
		stream := client.Open(ctx)
		if _, err := stream.Write(PCAPHeader()); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-stream.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation leaked request")
		}
		if !errors.Is(stream.Err(), context.Canceled) {
			t.Fatalf("got %v", stream.Err())
		}
	})
}

func TestRedirectDoesNotForwardChannel(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	_, err := Connect(context.Background(), fixtureChannel(first.URL), Options{})
	if !errors.Is(err, ErrRedirect) || reached.Load() {
		t.Fatalf("redirect forwarded: %v %v", err, reached.Load())
	}
}

func TestTLSVerificationIsNotDisabled(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS server reached handler") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	_, err := Connect(context.Background(), fixtureChannel(server.URL), Options{})
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("untrusted cert accepted: %v", err)
	}
}

func TestParseChannel(t *testing.T) {
	for _, raw := range []string{"http://user:pass@host", "http://host/?", "http://host/#", "http://host/?token=secret", "file:///tmp", "ftp://host", "http://host/other", "http://host/%6dulti/channel/ingest", "http://host:0", "http://host:65536", "http://host:", " http://host", "http://host\n", "http://host\\path"} {
		if _, _, err := ParseChannel(raw); err == nil {
			t.Error("unsafe channel accepted")
		}
	}
	for _, base := range []string{"http://host:8080", "https://host", "http://[::1]:8080"} {
		got, token, err := ParseChannel(fixtureChannel(base))
		if err != nil || got != base || token != testToken {
			t.Error("valid channel rejected")
		}
		if strings.Contains(RedactChannel(fixtureChannel(base)), testToken) {
			t.Error("redaction failed")
		}
	}
	for _, suffix := range []string{"", "%", "%ZZ", "%ff", "%C0%AF", "%ED%A0%80", "bad%00", "bad%09", "bad%0d%0aX-Header:yes", "bad%7f", "bad\n"} {
		if _, _, err := ParseChannel("http://host/multi/channel/ingest#" + suffix); err == nil {
			t.Error("invalid token accepted")
		}
	}
}

func TestCardChannelsWithoutFixedLength(t *testing.T) {
	for _, card := range []string{"x", "12345678", strings.Repeat("a", 32), testToken,
		strings.Repeat("a", 128), strings.Repeat("a", 256), strings.Repeat("a", 3000),
		"A+B/C=!?&", "卡密😀#%\\内部 空格", "literal%0d%0a"} {
		for _, route := range []string{"", "/", "/multi/channel/ingest"} {
			channel := "http://127.0.0.1:18880" + route + "#" + url.PathEscape(card)
			base, decoded, err := ParseChannel(channel)
			if err != nil || base != "http://127.0.0.1:18880" || decoded != card {
				t.Fatal("card was rejected or altered")
			}
			if RedactChannel(channel) != base+"/multi/channel/ingest" {
				t.Fatal("redacted channel exposed a credential")
			}
		}
	}
	_, card, err := ParseChannel("http://host#A+B/C=!?&")
	if err != nil || card != "A+B/C=!?&" {
		t.Fatal("printable card punctuation changed")
	}
}

func TestCardBearerBytesReachControlAndPCAP(t *testing.T) {
	for _, card := range []string{"x", strings.Repeat("a", 256), "卡密😀#%+?&=内部 空格", "literal%0d%0a"} {
		func() {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+card || r.URL.RawQuery != "" || r.URL.Fragment != "" {
					t.Error("credential bytes changed or leaked into request URL")
				}
				switch r.URL.Path {
				case "/multi/channel/status":
					io.WriteString(w, `{"running":false,"receiving":false}`)
				case "/multi/channel/start":
					io.Copy(io.Discard, r.Body)
					io.WriteString(w, `{"ok":true}`)
				case "/multi/channel/ingest":
					body, _ := io.ReadAll(r.Body)
					if !bytes.Equal(body, PCAPHeader()) {
						t.Error("PCAP bytes changed")
					}
					io.WriteString(w, `{"ok":true,"packets":0}`)
				default:
					t.Error("unexpected credential destination")
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := Connect(ctx, server.URL+"#"+url.PathEscape(card), Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			stream := client.Open(ctx)
			defer stream.Abort()
			if _, err = stream.Write(PCAPHeader()); err != nil {
				t.Fatal(err)
			}
			if result, err := stream.Close(); err != nil || !result.OK || requests.Load() != 3 {
				t.Fatal("card-authenticated upload did not complete")
			}
		}()
	}
}
