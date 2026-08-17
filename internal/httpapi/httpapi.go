// Package httpapi exposes the HTTP handlers for the Base62 codec and
// short-code registry service. It is a separate package so that both the
// main command (which serves it) and the self-check (which drives it through
// an in-process httptest.Server) can build identical routing without
// duplicating handler code.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"task051-base62/internal/base62"
	"task051-base62/internal/registry"
)

// Server wires the HTTP handlers to a single registry instance. Each
// NewServer call builds an isolated registry, so callers that need fresh
// state (such as the self-check) simply allocate a new Server.
type Server struct {
	reg *registry.Registry
}

// NewServer returns a Server backed by a fresh, empty registry.
func NewServer() *Server {
	return &Server{reg: registry.New()}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/encode", s.handleEncode)
	mux.HandleFunc("/decode", s.handleDecode)
	mux.HandleFunc("/alloc", s.handleAlloc)
	mux.HandleFunc("/reserve", s.handleReserve)
	mux.HandleFunc("/resolve", s.handleResolve)
	mux.HandleFunc("/alloc-batch", s.handleAllocBatch)
	mux.HandleFunc("/stats", s.handleStats)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- encode / decode ---

type encodeRequest struct {
	N uint64 `json:"n"`
}

type encodeResponse struct {
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
}

func (s *Server) handleEncode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	var req encodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errMsg("n 必须为 0 到 18446744073709551615 之间的非负整数"))
		return
	}
	writeJSON(w, http.StatusOK, encodeResponse{OK: true, Code: base62.Encode(req.N)})
}

type decodeRequest struct {
	Code string `json:"code"`
}

// decodeResponse uses *uint64 for N so that the value 0 is still serialized
// (a plain uint64 with omitempty would drop it, and without omitempty would
// emit a spurious 0 on error).
type decodeResponse struct {
	OK    bool    `json:"ok"`
	N     *uint64 `json:"n,omitempty"`
	Error string  `json:"error,omitempty"`
}

func (s *Server) handleDecode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	var req decodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, decodeResponse{OK: false, Error: "code 必须为非空 Base62 字符串"})
		return
	}
	n, err := base62.Decode(req.Code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, decodeResponse{OK: false, Error: errText(err)})
		return
	}
	writeJSON(w, http.StatusOK, decodeResponse{OK: true, N: &n})
}

// --- alloc / reserve / resolve / batch ---

type allocRequest struct {
	Source string `json:"source"`
}

type allocResponse struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleAlloc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	var req allocRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, allocResponse{OK: false, Error: "请求体必须是合法 JSON"})
		return
	}
	code, created, err := s.reg.Alloc(req.Source)
	if err != nil {
		writeJSON(w, errStatus(err), allocResponse{OK: false, Error: errText(err)})
		return
	}
	writeJSON(w, http.StatusOK, allocResponse{OK: true, Code: code, Created: created})
}

type reserveRequest struct {
	Source string `json:"source"`
	Code   string `json:"code"`
}

type reserveResponse struct {
	OK             bool   `json:"ok"`
	Code           string `json:"code"`
	Created        bool   `json:"created"`
	Error          string `json:"error,omitempty"`
	ConflictSource string `json:"conflict_source,omitempty"`
}

func (s *Server) handleReserve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	var req reserveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, reserveResponse{OK: false, Error: "请求体必须是合法 JSON"})
		return
	}
	created, err := s.reg.Reserve(req.Source, req.Code)
	if err != nil {
		resp := reserveResponse{OK: false, Code: req.Code, Error: errText(err)}
		if errors.Is(err, registry.ErrCollision) {
			var ce *registry.CollisionError
			if errors.As(err, &ce) {
				resp.ConflictSource = ce.ConflictSource
			}
			resp.Error = "碰撞"
		}
		writeJSON(w, errStatus(err), resp)
		return
	}
	writeJSON(w, http.StatusOK, reserveResponse{OK: true, Code: req.Code, Created: created})
}

type resolveResponse struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	code := r.URL.Query().Get("code")
	source, err := s.reg.Resolve(code)
	if err != nil {
		writeJSON(w, errStatus(err), resolveResponse{OK: false, Code: code, Error: errText(err)})
		return
	}
	writeJSON(w, http.StatusOK, resolveResponse{OK: true, Code: source, Source: source})
}

type allocBatchRequest struct {
	Sources []string `json:"sources"`
}

type allocBatchResponse struct {
	OK      bool                   `json:"ok"`
	Results []registry.AllocResult `json:"results,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

func (s *Server) handleAllocBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	var req allocBatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, allocBatchResponse{OK: false, Error: "请求体必须是合法 JSON"})
		return
	}
	results, err := s.reg.AllocBatch(req.Sources)
	if err != nil {
		writeJSON(w, errStatus(err), allocBatchResponse{OK: false, Error: errText(err)})
		return
	}
	writeJSON(w, http.StatusOK, allocBatchResponse{OK: true, Results: results[len(results):]})
}

// --- stats ---

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errMsg("method not allowed"))
		return
	}
	st := s.reg.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"sources":      st.Sources,
		"codes":        st.Codes,
		"next_counter": st.NextCounter,
		"collisions":   st.Collisions,
	})
}

// --- helpers ---

// errText maps a registry/base62 error to the Chinese error phrase used in
// JSON responses. The phrases mirror the problem statement so a caller can
// distinguish the documented outcomes.
func errText(err error) string {
	switch {
	case errors.Is(err, base62.ErrFormat), errors.Is(err, registry.ErrFormat):
		return "格式错误"
	case errors.Is(err, base62.ErrOverflow):
		return "溢出错误"
	case errors.Is(err, registry.ErrNotFound):
		return "未找到"
	case errors.Is(err, registry.ErrEmptySource):
		return "来源为空"
	case errors.Is(err, registry.ErrSourceTooLong):
		return "来源过长"
	case errors.Is(err, registry.ErrCollision):
		return "碰撞"
	default:
		return err.Error()
	}
}

// errStatus maps an error to its HTTP status code. A missing code is 404; a
// collision is 409; all other documented errors (format, overflow, source
// validation) are 400.
func errStatus(err error) int {
	switch {
	case errors.Is(err, registry.ErrNotFound):
		return http.StatusBadRequest
	case errors.Is(err, registry.ErrCollision):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// decodeJSON reads and unmarshals a bounded JSON request body into dst.
func decodeJSON(r *http.Request, dst any) error {
	const maxBody = 16 << 20
	lr := io.LimitReader(r.Body, maxBody+1)
	body, err := io.ReadAll(lr)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > maxBody {
		return fmt.Errorf("request body too large")
	}
	if len(body) == 0 {
		return fmt.Errorf("empty request body")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errMsg(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }
