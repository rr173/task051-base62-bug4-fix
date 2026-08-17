// Package selfcheck runs an end-to-end verification of the Base62 codec and
// short-code registry. It is invoked by the --smoke-test flag and exits the
// process on completion.
//
// Scenarios exercise the internal packages directly (for precise, JSON-free
// coverage of the codec and registry logic) and a final group drives the HTTP
// layer through an in-process httptest.Server so the handler wiring and error
// mapping are also covered. Every scenario uses a fresh registry or server so
// state never leaks between checks.
package selfcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"task051-base62/internal/base62"
	"task051-base62/internal/httpapi"
	"task051-base62/internal/registry"
)

// Run exercises the codec and registry across isolated scenarios, returning
// nil if every behavior matches the specification.
func Run() error {
	scenarios := []struct {
		name string
		fn   func() error
	}{
		// base62 codec
		{"编码规范", scenarioEncodeCanonical},
		{"解码合法值", scenarioDecodeValid},
		{"解码格式错误", scenarioDecodeFormatErrors},
		{"解码溢出错误", scenarioDecodeOverflow},
		{"大小写敏感", scenarioDecodeCaseSensitive},
		{"编解码往返一致", scenarioRoundTrip},
		{"uint64 上限编码", scenarioEncodeUint64Max},
		{"合法码判定", scenarioIsValid},
		// registry
		{"分配与幂等", scenarioAllocIdempotent},
		{"分配递增", scenarioAllocSequential},
		{"解析与未找到", scenarioResolve},
		{"预占成功", scenarioReserveNew},
		{"预占碰撞", scenarioReserveCollision},
		{"预占幂等", scenarioReserveIdempotent},
		{"预占格式错误", scenarioReserveFormatError},
		{"分配碰撞不变式", scenarioAllocCollisionGuard},
		{"批量分配去重", scenarioAllocBatchDedup},
		{"统计与双射", scenarioStats},
		// HTTP layer
		{"HTTP 编解码", scenarioHTTPEncodeDecode},
		{"HTTP 分配与解析", scenarioHTTPAllocResolve},
		{"HTTP 解码错误语义", scenarioHTTPDecodeErrors},
		{"HTTP 预占碰撞", scenarioHTTPReserveCollision},
	}
	for _, sc := range scenarios {
		if err := sc.fn(); err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
	}
	return nil
}

// ---------------- base62 codec scenarios ----------------

func scenarioEncodeCanonical() error {
	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0"},
		{9, "9"},
		{10, "A"},
		{35, "Z"},
		{36, "a"},
		{61, "z"},
		{62, "10"},
		{63, "11"},
	}
	for _, c := range cases {
		if got := base62.Encode(c.n); got != c.want {
			return fmt.Errorf("Encode(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	// Positive encodings never start with "0".
	for n := uint64(1); n < 5000; n++ {
		if e := base62.Encode(n); e[0] == '0' {
			return fmt.Errorf("Encode(%d) = %q has leading zero", n, e)
		}
	}
	return nil
}

func scenarioDecodeValid() error {
	cases := []struct {
		s    string
		want uint64
	}{
		{"0", 0},
		{"z", 61},
		{"10", 62},
		{"11", 63},
		{"A", 10},
		{"a", 36},
	}
	for _, c := range cases {
		got, err := base62.Decode(c.s)
		if err != nil {
			return fmt.Errorf("Decode(%q): %w", c.s, err)
		}
		if got != c.want {
			return fmt.Errorf("Decode(%q) = %d, want %d", c.s, got, c.want)
		}
	}
	return nil
}

func scenarioDecodeFormatErrors() error {
	for _, s := range []string{"", "abc!", "1-2", " 1", "1 ", "00", "07", "0A"} {
		if _, err := base62.Decode(s); !errors.Is(err, base62.ErrFormat) {
			return fmt.Errorf("Decode(%q): err=%v want ErrFormat", s, err)
		}
	}
	// The two error sentinels must be distinct.
	if errors.Is(base62.ErrFormat, base62.ErrOverflow) || errors.Is(base62.ErrOverflow, base62.ErrFormat) {
		return fmt.Errorf("error sentinels are not distinct")
	}
	return nil
}

func scenarioDecodeOverflow() error {
	// 11-char value exceeding uint64.
	if _, err := base62.Decode("zzzzzzzzzzz"); !errors.Is(err, base62.ErrOverflow) {
		return fmt.Errorf("Decode(zzzzzzzzzzz): err=%v want ErrOverflow", err)
	}
	// 12-char canonical string necessarily overflows.
	if _, err := base62.Decode("100000000000"); !errors.Is(err, base62.ErrOverflow) {
		return fmt.Errorf("Decode(100000000000): err=%v want ErrOverflow", err)
	}
	// 11-char value that does NOT overflow: 62^10.
	got, err := base62.Decode("10000000000")
	if err != nil {
		return fmt.Errorf("Decode(10000000000): %w", err)
	}
	if want := uint64(839299365868340224); got != want {
		return fmt.Errorf("Decode(10000000000) = %d, want %d", got, want)
	}
	return nil
}

func scenarioDecodeCaseSensitive() error {
	// 'A' and 'a' are different digits (10 vs 36).
	if v, _ := base62.Decode("A"); v != 10 {
		return fmt.Errorf("Decode(A) = %d, want 10", v)
	}
	if v, _ := base62.Decode("a"); v != 36 {
		return fmt.Errorf("Decode(a) = %d, want 36", v)
	}
	if base62.Encode(10) != "A" || base62.Encode(36) != "a" {
		return fmt.Errorf("Encode(10)=%q Encode(36)=%q, want A and a", base62.Encode(10), base62.Encode(36))
	}
	return nil
}

func scenarioRoundTrip() error {
	for _, v := range []uint64{0, 1, 61, 62, 63, 1000, 999999, 839299365868340223, 18446744073709551615} {
		enc := base62.Encode(v)
		got, err := base62.Decode(enc)
		if err != nil {
			return fmt.Errorf("Decode(%q): %w", enc, err)
		}
		if got != v {
			return fmt.Errorf("round-trip %d -> %q -> %d", v, enc, got)
		}
	}
	return nil
}

func scenarioEncodeUint64Max() error {
	maxU := uint64(18446744073709551615)
	enc := base62.Encode(maxU)
	if len(enc) != 11 {
		return fmt.Errorf("Encode(uint64max) = %q len %d, want 11", enc, len(enc))
	}
	got, err := base62.Decode(enc)
	if err != nil {
		return fmt.Errorf("Decode(uint64max enc): %w", err)
	}
	if got != maxU {
		return fmt.Errorf("uint64max round-trip got %d", got)
	}
	// Boundary: 62^10 - 1 is the largest 10-char value, 62^10 is the smallest
	// 11-char value.
	if e := base62.Encode(839299365868340223); len(e) != 10 || e != "zzzzzzzzzz" {
		return fmt.Errorf("Encode(62^10-1) = %q, want 10-char zzzzzzzzzz", e)
	}
	if e := base62.Encode(839299365868340224); len(e) != 11 || e != "10000000000" {
		return fmt.Errorf("Encode(62^10) = %q, want 11-char 10000000000", e)
	}
	return nil
}

func scenarioIsValid() error {
	for _, s := range []string{"0", "10", "A", "z", "zz", "LygHa16AHYF", "10000000000"} {
		if !base62.IsValid(s) {
			return fmt.Errorf("IsValid(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "00", "07", "ab!", "zzzzzzzzzzz", "100000000000"} {
		if base62.IsValid(s) {
			return fmt.Errorf("IsValid(%q) = true, want false", s)
		}
	}
	return nil
}

// ---------------- registry scenarios ----------------

func scenarioAllocIdempotent() error {
	r := registry.New()
	c1, created1, err := r.Alloc("A")
	if err != nil {
		return fmt.Errorf("alloc A: %w", err)
	}
	if c1 != "0" || !created1 {
		return fmt.Errorf("alloc A = %q created=%v, want 0/true", c1, created1)
	}
	c2, created2, err := r.Alloc("A")
	if err != nil {
		return fmt.Errorf("alloc A again: %w", err)
	}
	if c2 != "0" || created2 {
		return fmt.Errorf("alloc A again = %q created=%v, want 0/false", c2, created2)
	}
	if st := r.Stats(); st.NextCounter != 1 {
		return fmt.Errorf("counter = %d, want 1", st.NextCounter)
	}
	return nil
}

func scenarioAllocSequential() error {
	r := registry.New()
	for i, want := range []string{"0", "1", "2", "3", "4"} {
		src := string(rune('A' + i))
		c, _, err := r.Alloc(src)
		if err != nil {
			return fmt.Errorf("alloc %s: %w", src, err)
		}
		if c != want {
			return fmt.Errorf("alloc %s = %q, want %q", src, c, want)
		}
	}
	return nil
}

func scenarioResolve() error {
	r := registry.New()
	r.Alloc("A")
	r.Alloc("B")
	if src, err := r.Resolve("0"); err != nil || src != "A" {
		return fmt.Errorf("Resolve(0) = %q err=%v, want A", src, err)
	}
	// Well-formed but unbound -> NotFound.
	if _, err := r.Resolve("zz"); !errors.Is(err, registry.ErrNotFound) {
		return fmt.Errorf("Resolve(zz): err=%v want ErrNotFound", err)
	}
	// Malformed -> Format, distinct from NotFound.
	if _, err := r.Resolve("00"); !errors.Is(err, registry.ErrFormat) {
		return fmt.Errorf("Resolve(00): err=%v want ErrFormat", err)
	}
	return nil
}

func scenarioReserveNew() error {
	r := registry.New()
	created, err := r.Reserve("X", "abc")
	if err != nil {
		return fmt.Errorf("reserve: %w", err)
	}
	if !created {
		return fmt.Errorf("created=false, want true")
	}
	if src, err := r.Resolve("abc"); err != nil || src != "X" {
		return fmt.Errorf("Resolve(abc) = %q err=%v, want X", src, err)
	}
	return nil
}

func scenarioReserveCollision() error {
	r := registry.New()
	if _, err := r.Reserve("X", "abc"); err != nil {
		return err
	}
	_, err := r.Reserve("Y", "abc")
	var ce *registry.CollisionError
	if !errors.As(err, &ce) {
		return fmt.Errorf("reserve Y: err=%v want *CollisionError", err)
	}
	if ce.ConflictSource != "X" {
		return fmt.Errorf("conflict source = %q, want X", ce.ConflictSource)
	}
	if st := r.Stats(); st.Collisions != 1 {
		return fmt.Errorf("collisions = %d, want 1", st.Collisions)
	}
	return nil
}

func scenarioReserveIdempotent() error {
	r := registry.New()
	r.Reserve("X", "abc")
	created, err := r.Reserve("X", "abc")
	if err != nil {
		return fmt.Errorf("reserve X again: %w", err)
	}
	if created {
		return fmt.Errorf("created=true, want false for idempotent reserve")
	}
	if st := r.Stats(); st.Collisions != 0 {
		return fmt.Errorf("collisions = %d, want 0", st.Collisions)
	}
	return nil
}

func scenarioReserveFormatError() error {
	r := registry.New()
	for _, bad := range []string{"", "00", "07", "ab!"} {
		if _, err := r.Reserve("X", bad); !errors.Is(err, registry.ErrFormat) {
			return fmt.Errorf("Reserve(code=%q): err=%v want ErrFormat", bad, err)
		}
	}
	return nil
}

func scenarioAllocCollisionGuard() error {
	r := registry.New()
	// Reserve the code the counter would mint first (Encode(0) == "0").
	if _, err := r.Reserve("A", "0"); err != nil {
		return err
	}
	// Allocating a different source collides without advancing the counter.
	_, _, err := r.Alloc("B")
	var ce *registry.CollisionError
	if !errors.As(err, &ce) {
		return fmt.Errorf("alloc B: err=%v want *CollisionError", err)
	}
	if ce.ConflictSource != "A" || ce.Code != "0" {
		return fmt.Errorf("collision = %+v, want code=0 conflict=A", ce)
	}
	st := r.Stats()
	if st.NextCounter != 0 {
		return fmt.Errorf("counter advanced to %d, want 0", st.NextCounter)
	}
	if st.Collisions != 1 {
		return fmt.Errorf("collisions = %d, want 1", st.Collisions)
	}
	// Already-bound source served idempotently.
	if c, created, err := r.Alloc("A"); err != nil || c != "0" || created {
		return fmt.Errorf("alloc A after collision = %q created=%v err=%v, want 0/false", c, created, err)
	}
	return nil
}

func scenarioAllocBatchDedup() error {
	r := registry.New()
	res, err := r.AllocBatch([]string{"A", "B", "A"})
	if err != nil {
		return fmt.Errorf("batch: %w", err)
	}
	want := []registry.AllocResult{
		{Source: "A", Code: "0", Created: true},
		{Source: "B", Code: "1", Created: true},
		{Source: "A", Code: "0", Created: false},
	}
	for i, w := range want {
		if res[i] != w {
			return fmt.Errorf("result[%d] = %+v, want %+v", i, res[i], w)
		}
	}
	st := r.Stats()
	if st.Sources != 2 || st.Codes != 2 || st.NextCounter != 2 {
		return fmt.Errorf("stats = %+v, want sources=codes=2 next=2", st)
	}
	return nil
}

func scenarioStats() error {
	r := registry.New()
	r.Alloc("A")
	r.Alloc("B")
	r.Reserve("C", "zz")
	// A failed reserve (collision) must increment collisions without adding a
	// binding.
	r.Reserve("D", "zz")
	st := r.Stats()
	if st.Sources != st.Codes {
		return fmt.Errorf("bijection broken: sources=%d codes=%d", st.Sources, st.Codes)
	}
	if st.Sources != 3 {
		return fmt.Errorf("sources = %d, want 3", st.Sources)
	}
	if st.Collisions != 1 {
		return fmt.Errorf("collisions = %d, want 1", st.Collisions)
	}
	if st.NextCounter != 2 {
		return fmt.Errorf("next_counter = %d, want 2 (reserve does not advance)", st.NextCounter)
	}
	return nil
}

// ---------------- HTTP layer scenarios ----------------

// httpServer stands up a fresh server backed by an isolated registry.
type httpServer struct {
	srv *httptest.Server
}

func newHTTPServer() *httpServer {
	api := httpapi.NewServer()
	return &httpServer{srv: httptest.NewServer(api.Handler())}
}

func (h *httpServer) close() { h.srv.Close() }

// doJSON issues a request with an optional JSON body and returns the status
// code and response body bytes.
func (h *httpServer) doJSON(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func scenarioHTTPEncodeDecode() error {
	h := newHTTPServer()
	defer h.close()

	status, body, err := h.doJSON(http.MethodPost, "/encode", map[string]any{"n": 62})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("encode status = %d, body=%s", status, body)
	}
	var enc struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &enc); err != nil {
		return fmt.Errorf("decode encode resp: %w", err)
	}
	if !enc.OK || enc.Code != "10" {
		return fmt.Errorf("encode resp = %+v, want ok code=10", enc)
	}

	status, body, err = h.doJSON(http.MethodPost, "/decode", map[string]any{"code": "10"})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("decode status = %d", status)
	}
	var dec struct {
		OK bool    `json:"ok"`
		N *uint64  `json:"n"`
	}
	if err := json.Unmarshal(body, &dec); err != nil {
		return fmt.Errorf("decode decode resp: %w", err)
	}
	if !dec.OK || dec.N == nil || *dec.N != 62 {
		return fmt.Errorf("decode resp = %+v, want ok n=62", dec)
	}

	// decode("0") must report n=0 explicitly (no omitempty elision).
	status, body, err = h.doJSON(http.MethodPost, "/decode", map[string]any{"code": "0"})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &dec); err != nil {
		return err
	}
	if !dec.OK || dec.N == nil || *dec.N != 0 {
		return fmt.Errorf("decode(\"0\") resp = %+v, want ok n=0 present", dec)
	}
	return nil
}

func scenarioHTTPAllocResolve() error {
	h := newHTTPServer()
	defer h.close()

	status, body, err := h.doJSON(http.MethodPost, "/alloc", map[string]any{"source": "A"})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("alloc status = %d", status)
	}
	var ar struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(body, &ar); err != nil {
		return err
	}
	if !ar.OK || ar.Code != "0" || !ar.Created {
		return fmt.Errorf("alloc resp = %+v, want ok code=0 created=true", ar)
	}

	// Idempotent second alloc.
	status, body, err = h.doJSON(http.MethodPost, "/alloc", map[string]any{"source": "A"})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &ar); err != nil {
		return err
	}
	if !ar.OK || ar.Code != "0" || ar.Created {
		return fmt.Errorf("alloc-again resp = %+v, want ok code=0 created=false", ar)
	}

	// Resolve via GET.
	status, body, err = h.doJSON(http.MethodGet, "/resolve?code=0", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("resolve status = %d", status)
	}
	var rr struct {
		OK     bool   `json:"ok"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(body, &rr); err != nil {
		return err
	}
	if !rr.OK || rr.Source != "A" {
		return fmt.Errorf("resolve resp = %+v, want ok source=A", rr)
	}

	// Stats reflect one source, one code, counter advanced once.
	status, body, err = h.doJSON(http.MethodGet, "/stats", nil)
	if err != nil {
		return err
	}
	var st struct {
		Sources     int    `json:"sources"`
		Codes       int    `json:"codes"`
		NextCounter uint64 `json:"next_counter"`
		Collisions  int64  `json:"collisions"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return err
	}
	if st.Sources != 1 || st.Codes != 1 || st.NextCounter != 1 || st.Collisions != 0 {
		return fmt.Errorf("stats = %+v, want sources=codes=1 next=1 collisions=0", st)
	}
	return nil
}

func scenarioHTTPDecodeErrors() error {
	h := newHTTPServer()
	defer h.close()

	// Format error: leading zero on a multi-char string.
	status, body, err := h.doJSON(http.MethodPost, "/decode", map[string]any{"code": "00"})
	if err != nil {
		return err
	}
	if status != http.StatusBadRequest {
		return fmt.Errorf("decode 00 status = %d, want 400", status)
	}
	var de struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &de); err != nil {
		return err
	}
	if de.OK || de.Error != "格式错误" {
		return fmt.Errorf("decode 00 resp = %+v, want ok=false error=格式错误", de)
	}

	// Overflow error: 11-char value exceeding uint64.
	status, body, err = h.doJSON(http.MethodPost, "/decode", map[string]any{"code": "zzzzzzzzzzz"})
	if err != nil {
		return err
	}
	if status != http.StatusBadRequest {
		return fmt.Errorf("decode overflow status = %d, want 400", status)
	}
	if err := json.Unmarshal(body, &de); err != nil {
		return err
	}
	if de.OK || de.Error != "溢出错误" {
		return fmt.Errorf("decode overflow resp = %+v, want ok=false error=溢出错误", de)
	}
	return nil
}

func scenarioHTTPReserveCollision() error {
	h := newHTTPServer()
	defer h.close()

	status, body, err := h.doJSON(http.MethodPost, "/reserve", map[string]any{"source": "X", "code": "abc"})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("reserve X status = %d, want 200", status)
	}

	// Collision: same code, different source.
	status, body, err = h.doJSON(http.MethodPost, "/reserve", map[string]any{"source": "Y", "code": "abc"})
	if err != nil {
		return err
	}
	if status != http.StatusConflict {
		return fmt.Errorf("reserve Y status = %d, want 409", status)
	}
	var rv struct {
		OK             bool   `json:"ok"`
		Error          string `json:"error"`
		ConflictSource string `json:"conflict_source"`
	}
	if err := json.Unmarshal(body, &rv); err != nil {
		return err
	}
	if rv.OK || rv.Error != "碰撞" || rv.ConflictSource != "X" {
		return fmt.Errorf("reserve Y resp = %+v, want ok=false error=碰撞 conflict=X", rv)
	}
	return nil
}
