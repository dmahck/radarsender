package sender

import (
	"encoding/json"
	"io"
	"net/http"
)

var Methods = map[string]any{
	"status":    map[string]any{},
	"channel":   map[string]any{},
	"configure": map[string]any{"channel": ""},
	"start":     map[string]any{},
	"stop":      map[string]any{},
}

type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func Failure(message string) map[string]any { return map[string]any{"ok": false, "error": message} }
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		respond := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.Method != http.MethodPost || r.URL.Path != "/rpc" {
			w.WriteHeader(400)
			respond(Failure("无效管理请求"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		defer r.Body.Close()
		var req Request
		decoder := json.NewDecoder(r.Body)
		if decoder.Decode(&req) != nil {
			w.WriteHeader(400)
			respond(Failure("管理请求格式错误或超过大小限制"))
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			w.WriteHeader(400)
			respond(Failure("管理请求包含多余内容"))
			return
		}
		var err error
		switch req.Method {
		case "status":
			respond(s.Snapshot())
			return
		case "channel":
			respond(map[string]any{"ok": true, "channel": s.Channel()})
			return
		case "configure":
			var c Config
			if json.Unmarshal(req.Params, &c) != nil {
				respond(Failure("设置格式无效"))
				return
			}
			err = s.Configure(c)
		case "start":
			err = s.Start()
		case "stop":
			s.Stop()
		default:
			respond(Failure("不支持的操作"))
			return
		}
		if err != nil {
			respond(Failure(err.Error()))
			return
		}
		respond(map[string]bool{"ok": true})
	})
}
