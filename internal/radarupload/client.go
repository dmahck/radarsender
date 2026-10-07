// Package radarupload connects a channel-authenticated sender to the radar gateway.
// It never retries authentication or replays a partially uploaded capture.
package radarupload

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const FullMessage = "当前服务器已满员，请联系代理更换服务器"

var (
	ErrURL       = errors.New("连接通道格式无效，请从用户页面重新复制完整通道")
	ErrChannel   = errors.New("请粘贴用户页面提供的完整连接通道")
	ErrAuth      = errors.New("连接通道无效、已到期或已被更新，请登录用户页面重新复制")
	ErrFull      = errors.New(FullMessage)
	ErrConflict  = errors.New("本账号已有流量输入，或雷达状态已改变，请停止其他发送端后重试")
	ErrForbidden = errors.New("连接通道验证失败，请重新复制通道")
	ErrRate      = errors.New("连接尝试过于频繁，请稍后重试")
	ErrNetwork   = errors.New("雷达连接中断或超时，请检查服务器地址与网络")
	ErrProtocol  = errors.New("雷达服务器响应无效，请确认连接地址和服务端版本")
	ErrCapture   = errors.New("雷达服务器拒绝抓包数据，请检查 Ethernet PCAP 格式")
	ErrServer    = errors.New("雷达服务器暂不可用")
	ErrRedirect  = errors.New("雷达地址发生跳转，请填写最终服务器地址")
	ErrEarlyEnd  = errors.New("雷达服务器提前结束上传")
)

// ParseChannel accepts compact origins and legacy ingest URLs, separating the
// bearer so it never appears in any request URL, query, error, or status response.
func ParseChannel(raw string) (base, token string, err error) {
	if raw != strings.TrimSpace(raw) || len(raw) > 4096 {
		return "", "", ErrChannel
	}
	endpoint, fragment, found := strings.Cut(raw, "#")
	if !found || strings.ContainsAny(endpoint, "\\\r\n\t?% ") {
		return "", "", ErrChannel
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.User != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawPath != "" || (u.Path != "" && u.Path != "/" && u.Path != "/multi/channel/ingest") {
		return "", "", ErrChannel
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", "", ErrChannel
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", "", ErrChannel
	}
	// Decode exactly once, preserving '+' and allowing arbitrary card lengths.
	// The server authenticates the credential; only HTTP header safety is local.
	token, e = url.PathUnescape(fragment)
	if e != nil || token == "" || !utf8.ValidString(token) {
		return "", "", ErrChannel
	}
	for _, r := range token {
		if r < 0x20 || r == 0x7f {
			return "", "", ErrChannel
		}
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), token, nil
}

func RedactChannel(raw string) string {
	base, _, err := ParseChannel(raw)
	if err != nil {
		return ""
	}
	return base + "/multi/channel/ingest"
}

type Options struct {
	// OnConnect is invoked before any authentication or upload bytes are sent.
	// Capture consumers can exclude this exact TCP connection from mirroring.
	OnConnect func(local, remote net.Addr)
	// SenderID remains stable across retries within one sending session.
	SenderID string
	// AllowExistingInput is only for a retry of SenderID. The server still
	// rejects a different sender identity and never permits two active uploads.
	AllowExistingInput bool
}

func NewSenderID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	value := hex.EncodeToString(id[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], nil
}

type Client struct {
	base      string
	token     string
	senderID  string
	http      *http.Client
	transport *http.Transport
}

type deadlineConn struct {
	net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

func (c *deadlineConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *deadlineConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// Connect verifies a private channel once and starts an idle account's engine.
// It neither logs in with a card nor reads or transmits browser cookies.
func Connect(ctx context.Context, channel string, options Options) (*Client, error) {
	base, token, err := ParseChannel(channel)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil, // Never silently forward a channel bearer via an environment proxy.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if options.OnConnect != nil {
				options.OnConnect(conn.LocalAddr(), conn.RemoteAddr())
			}
			return &deadlineConn{Conn: conn, closed: make(chan struct{})}, nil
		},
		TLSHandshakeTimeout:    10 * time.Second,
		IdleConnTimeout:        30 * time.Second,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        1,
		MaxResponseHeaderBytes: 16 << 10,
	}
	c := &Client{base: base, token: token, senderID: options.SenderID, transport: transport}
	c.http = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ok := false
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	var status struct {
		Running   *bool `json:"running"`
		Receiving bool  `json:"receiving"`
	}
	if err = c.control(ctx, http.MethodGet, "/multi/channel/status", nil, &status); err != nil {
		return nil, err
	}
	if status.Running == nil {
		return nil, ErrProtocol
	}
	if status.Receiving && !options.AllowExistingInput {
		return nil, ErrConflict
	}
	if !*status.Running {
		var started struct {
			OK bool `json:"ok"`
		}
		if err = c.control(ctx, http.MethodPost, "/multi/channel/start", map[string]any{}, &started); err != nil {
			return nil, err
		}
		if !started.OK {
			return nil, ErrProtocol
		}
	}
	ok = true
	return c, nil
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }

func (c *Client) control(parent context.Context, method, path string, value, result any) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	var body io.Reader
	if value != nil {
		data, _ := json.Marshal(value)
		body = bytes.NewReader(data)
	}
	req, _ := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		return ErrNetwork
	}
	defer response.Body.Close()
	data, err := readResponse(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return responseError(response.StatusCode, data)
	}
	if json.Unmarshal(data, result) != nil {
		return ErrProtocol
	}
	return nil
}

func readResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, 65537))
	if err != nil {
		return nil, ErrNetwork
	}
	if len(data) > 65536 {
		return nil, ErrProtocol
	}
	return data, nil
}

func responseError(code int, body []byte) error {
	if code >= 300 && code <= 399 {
		return ErrRedirect
	}
	switch code {
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusConflict:
		return ErrConflict
	case http.StatusTooManyRequests:
		return ErrRate
	case http.StatusBadRequest, http.StatusUnsupportedMediaType:
		return ErrCapture
	case http.StatusServiceUnavailable:
		message := strings.TrimSpace(string(body))
		var v struct {
			Error string `json:"error"`
		}
		if message == FullMessage || (json.Unmarshal(body, &v) == nil && v.Error == FullMessage) {
			return ErrFull
		}
	}
	return ErrServer
}

type Result struct {
	OK      bool   `json:"ok"`
	Packets uint64 `json:"packets"`
}

// Stream has one serial writer and an independent HTTP response reader, so an
// early 401/409 stops an idle capture as well as an actively writing capture.
// There is no total upload timeout. Individual writes and final EOF are bounded.
type Stream struct {
	writer    *io.PipeWriter
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	ending    bool
	result    Result
	err       error
	closeOnce sync.Once
}

func (c *Client) Open(ctx context.Context) *Stream {
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	s := &Stream{writer: writer, cancel: cancel, done: make(chan struct{})}
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		conn := info.Conn
		if tls, ok := conn.(interface{ NetConn() net.Conn }); ok {
			conn = tls.NetConn()
		}
		if tracked, ok := conn.(*deadlineConn); ok {
			go func() {
				select {
				case <-tracked.closed:
					// Transport waits for its body writer before returning a network
					// error. Unblock that writer even when capture is currently idle.
					_ = reader.CloseWithError(ErrNetwork)
				case <-ctx.Done():
					_ = reader.CloseWithError(ctx.Err())
				}
			}()
		}
	}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/multi/channel/ingest", reader)
	req.Header.Set("Content-Type", "application/vnd.tcpdump.pcap")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.senderID != "" {
		req.Header.Set("X-Radar-Sender", c.senderID)
	}
	// A streamed reader is intentionally not rewindable: Transport cannot retry
	// its body after a partial network write.
	go func() {
		defer cancel()
		response, err := c.http.Do(req)
		var result Result
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ErrNetwork
			}
		} else {
			// A response has started: final/error bodies must not be allowed to
			// keep the producer alive indefinitely after an early rejection.
			bodyTimer := time.AfterFunc(10*time.Second, cancel)
			data, readErr := readResponse(response.Body)
			bodyTimer.Stop()
			response.Body.Close()
			err = readErr
			if err == nil {
				if response.StatusCode != http.StatusOK {
					err = responseError(response.StatusCode, data)
				} else {
					var confirmed struct {
						OK      *bool   `json:"ok"`
						Packets *uint64 `json:"packets"`
					}
					if json.Unmarshal(data, &confirmed) != nil || confirmed.OK == nil || !*confirmed.OK || confirmed.Packets == nil {
						err = ErrProtocol
					} else {
						result = Result{OK: true, Packets: *confirmed.Packets}
					}
				}
			}
		}
		s.mu.Lock()
		if err == nil && !s.ending {
			err = ErrEarlyEnd
		}
		s.result, s.err = result, err
		s.mu.Unlock()
		if err != nil {
			_ = reader.CloseWithError(err)
		} else {
			_ = reader.Close()
		}
		close(s.done)
	}()
	return s
}

func (s *Stream) Done() <-chan struct{} { return s.done }

func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Stream) Write(data []byte) (int, error) {
	n, err := s.writer.Write(data)
	if err != nil {
		// net/http can close the request pipe before our response goroutine has
		// classified an early 401/409. Preserve that decisive status instead of
		// turning it into a generic closed-pipe error and cancelling its body.
		timer := time.NewTimer(11 * time.Second)
		defer timer.Stop()
		select {
		case <-s.done:
			if streamErr := s.Err(); streamErr != nil {
				return n, streamErr
			}
		case <-timer.C:
			s.Abort()
		}
		return n, ErrNetwork
	}
	return n, err
}

func (s *Stream) Abort() {
	s.cancel()
	_ = s.writer.CloseWithError(context.Canceled)
}

func (s *Stream) Close() (Result, error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.ending = true
		s.mu.Unlock()
		_ = s.writer.Close()
	})
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
		s.Abort()
		<-s.done
		return Result{}, ErrNetwork
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.err
}
