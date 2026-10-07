package sender

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"radarsender/internal/radarupload"
)

const Version = "0.1.5"

type Config struct {
	Interface string `json:"-"` // Resolved per attempt; never loaded from legacy configuration.
	Channel   string `json:"channel,omitempty"`
}

func Defaults() Config { return Config{} }

func (c Config) Validate() error {
	if c.Channel != "" {
		_, _, err := radarupload.ParseChannel(c.Channel)
		return err
	}
	return nil
}

type Counters struct {
	Captured       uint64 `json:"captured"`
	OfflineDropped uint64 `json:"offline_dropped"`
	Packets        uint64 `json:"packets"`
	Bytes          uint64 `json:"bytes"`
	Dropped        uint64 `json:"dropped"`
	Errors         uint64 `json:"errors"`
}

func (a Counters) plus(b Counters) Counters {
	return Counters{Captured: a.Captured + b.Captured, OfflineDropped: a.OfflineDropped + b.OfflineDropped, Packets: a.Packets + b.Packets, Bytes: a.Bytes + b.Bytes, Dropped: a.Dropped + b.Dropped, Errors: a.Errors + b.Errors}
}

type Status struct {
	OK          bool   `json:"ok"`
	Version     string `json:"version"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	ConfigError string `json:"config_error,omitempty"`
	Config      Config `json:"config"`
	HasChannel  bool   `json:"has_channel"`
	Endpoint    string `json:"endpoint,omitempty"`
	Interface   string `json:"interface,omitempty"`
	Capturing   bool   `json:"capturing"`
	Counters
	Attempts     int `json:"attempts"`
	RetrySeconds int `json:"retry_seconds"`
}
type attemptFunc func(context.Context, Config, string, bool, func(), func(Counters)) (Counters, error)
type Service struct {
	mu      sync.Mutex
	dir     string
	config  Config
	status  Status
	cancel  context.CancelFunc
	done    chan struct{}
	attempt attemptFunc
	capture *captureSession
	detect  func(context.Context) (string, error)
	retry   time.Duration
	closed  bool
}

func New(dir string) *Service {
	s := &Service{dir: dir, config: Defaults(), detect: detectLAN, retry: 2 * time.Second}
	s.status = Status{OK: true, Version: Version, State: "idle"}
	f, err := os.Open(filepath.Join(dir, "config.json"))
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(f, 8193))
		_ = f.Close()
	}
	if err == nil {
		c := Defaults()
		if len(b) > 8192 || json.Unmarshal(b, &c) != nil {
			s.status.ConfigError = "配置文件损坏，请重新粘贴通道并点击连接"
		} else if err = c.Validate(); err != nil {
			s.status.ConfigError = err.Error()
		} else {
			s.config = c
		}
	} else if !os.IsNotExist(err) {
		s.status.ConfigError = "无法读取配置文件，请检查 /etc/radarsender 权限"
	}
	return s
}

func (s *Service) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.status
	if s.capture != nil {
		counts, active := s.capture.stats()
		v.Counters = v.Counters.plus(counts)
		v.Capturing = active
	}
	v.Config = s.config
	v.Config.Channel = ""
	v.HasChannel = s.config.Channel != ""
	if v.HasChannel {
		v.Endpoint = radarupload.RedactChannel(s.config.Channel)
	}
	return v
}

func (s *Service) Configure(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil || s.closed {
		return errors.New("请先断开发送再修改设置")
	}
	if c.Channel == "" {
		c.Channel = s.config.Channel
	}
	c.Interface = ""
	if err := c.Validate(); err != nil {
		return err
	}
	if err := saveConfig(s.dir, c); err != nil {
		return errors.New("配置保存失败，请检查配置目录权限及剩余空间")
	}
	s.config = c
	s.status.ConfigError, s.status.Error, s.status.State = "", "", "idle"
	return nil
}

func saveConfig(dir string, c Config) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "config.json"))
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil || s.closed {
		return errors.New("发送任务已在运行或服务正在关闭")
	}
	if s.status.ConfigError != "" {
		return errors.New(s.status.ConfigError)
	}
	if err := s.config.Validate(); err != nil {
		return err
	}
	if s.config.Channel == "" {
		return radarupload.ErrChannel
	}
	id, err := radarupload.NewSenderID()
	if err != nil {
		return errors.New("无法生成连接标识")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.capture = &captureSession{parent: ctx}
	s.status = Status{OK: true, Version: Version, State: "connecting"}
	go s.run(ctx, s.config, id, done)
	return nil
}
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.status.State = "stopping"
		s.status.RetrySeconds = 0
		s.cancel()
	}
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	done := s.done
	if s.cancel != nil {
		s.status.State = "stopping"
		s.cancel()
	}
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func retryable(err error) bool {
	return errors.Is(err, errLANUnavailable) || errors.Is(err, radarupload.ErrNetwork) || errors.Is(err, radarupload.ErrEarlyEnd) || errors.Is(err, radarupload.ErrServer) || errors.Is(err, radarupload.ErrRate) || errors.Is(err, radarupload.ErrFull)
}
func (s *Service) run(ctx context.Context, c Config, id string, done chan struct{}) {
	var final error
	defer func() {
		s.capture.close()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.status.State, s.status.Error, s.status.RetrySeconds = "idle", "", 0
		if final != nil {
			s.status.State, s.status.Error = "error", final.Error()
		}
		s.cancel()
		s.cancel, s.done = nil, nil
		close(done)
	}()
	totals := Counters{}
	uploadAttempted := false
	attemptFn := s.attempt
	if attemptFn == nil {
		attemptFn = func(ctx context.Context, c Config, id string, retry bool, ready func(), report func(Counters)) (Counters, error) {
			return runAttempt(ctx, c, id, retry, ready, report, s.capture)
		}
	}
	for attempt := 0; ctx.Err() == nil; attempt++ {
		s.mu.Lock()
		s.status.Attempts = attempt + 1
		s.mu.Unlock()
		var counts Counters
		name, err := s.detect(ctx)
		if err == nil {
			c.Interface = name
			s.mu.Lock()
			s.status.Interface = name
			s.mu.Unlock()
			counts, err = attemptFn(ctx, c, id, uploadAttempted, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if ctx.Err() == nil {
					s.status.State, s.status.Error, s.status.RetrySeconds = "sending", "", 0
				}
			}, func(counts Counters) { s.mu.Lock(); s.status.Counters = totals.plus(counts); s.mu.Unlock() })
			uploadAttempted = true
		}
		totals = totals.plus(counts)
		s.mu.Lock()
		s.status.Counters = totals
		s.mu.Unlock()
		if ctx.Err() != nil {
			if err != nil && !errors.Is(err, context.Canceled) {
				final = err
			}
			return
		}
		if err == nil {
			err = radarupload.ErrEarlyEnd
		}
		if counts.Errors == 0 {
			totals.Errors++
		}
		if !retryable(err) {
			s.mu.Lock()
			s.status.Counters = totals
			s.mu.Unlock()
			final = err
			return
		}
		s.mu.Lock()
		s.status.Counters = totals
		if ctx.Err() == nil {
			s.status.State, s.status.Error, s.status.RetrySeconds = "reconnecting", err.Error(), int(s.retry/time.Second)
		}
		s.mu.Unlock()
		timer := time.NewTimer(s.retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
