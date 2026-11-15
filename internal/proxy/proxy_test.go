package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/policy"
)

func params(raw string) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(err)
	}
	return out
}

func defaultParams() map[string]json.RawMessage {
	return params(`{"temperature":0.2,"top_p":0.9,"max_tokens":96,"keep_alive":"30m"}`)
}

func newUpstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func newProxy(t *testing.T, upstream *httptest.Server, mutate func(*Options)) *httptest.Server {
	t.Helper()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Upstream:       u,
		Timeout:        5 * time.Second,
		Params:         defaultParams(),
		MaxConcurrent:  8,
		MaxBodySize:    1 << 20,
		AcquireTimeout: 200 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&opts)
	}
	h, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		srv.Close()
		h.Close()
	})
	return srv
}

// echoHandler reports back what the upstream actually received.
func echoHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("upstream read body: %v", err)
		}
		payload := map[string]any{
			"method":  r.Method,
			"uri":     r.URL.RequestURI(),
			"body":    string(body),
			"headers": r.Header,
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func do(t *testing.T, method, target, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, data
}

func post(t *testing.T, target, body string) (*http.Response, []byte) {
	t.Helper()
	return do(t, http.MethodPost, target, body, map[string]string{"Content-Type": "application/json"})
}

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return out
}

// forwardedBody returns the body the upstream received, as a generic map.
func forwardedBody(t *testing.T, echo []byte) map[string]any {
	t.Helper()
	envelope := decodeMap(t, echo)
	raw, ok := envelope["body"].(string)
	if !ok {
		t.Fatalf("echo has no body: %v", envelope)
	}
	return decodeMap(t, []byte(raw))
}

func TestV1Injection(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	resp, data := post(t, p.URL+"/v1/chat/completions", `{"model":"m","temperature":1}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, data)
	}
	got := forwardedBody(t, data)
	if got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want none", got["reasoning_effort"])
	}
	if got["temperature"] != float64(1) {
		t.Errorf("temperature = %v, want client value 1", got["temperature"])
	}
	if got["max_tokens"] != float64(96) {
		t.Errorf("max_tokens = %v, want injected 96", got["max_tokens"])
	}
	if got["top_p"] != 0.9 || got["keep_alive"] != "30m" || got["model"] != "m" {
		t.Errorf("unexpected injected body: %v", got)
	}
	if _, present := got["think"]; present {
		t.Error("think must be removed on /v1")
	}
}

func TestV1ForcesOffEvenWhenClientAsksForThinking(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	_, data := post(t, p.URL+"/v1/chat/completions", `{"model":"m","reasoning_effort":"high","think":true}`)
	got := forwardedBody(t, data)
	if got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want forced none", got["reasoning_effort"])
	}
	if _, present := got["think"]; present {
		t.Error("think must be removed even if the client sent it")
	}
}

func TestNativeInjection(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	_, data := post(t, p.URL+"/api/chat", `{"model":"m","think":true,"messages":[]}`)
	got := forwardedBody(t, data)
	if got["think"] != false {
		t.Errorf("think = %v, want false", got["think"])
	}
	options, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %v", got["options"])
	}
	if options["num_predict"] != float64(96) || options["temperature"] != 0.2 {
		t.Errorf("options = %v", options)
	}
	if got["keep_alive"] != "30m" {
		t.Errorf("keep_alive = %v, want top level", got["keep_alive"])
	}
	if _, present := got["reasoning_effort"]; present {
		t.Error("reasoning_effort must be removed on /api/chat")
	}
}

func TestUnknownFieldsAndNumbersSurvive(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	body := `{"model":"m","big":12345678901234567890,"pi":3.14159265358979323846,` +
		`"future":{"nested":[1,2,{"x":null}]}}`
	_, data := post(t, p.URL+"/v1/chat/completions", body)
	got := forwardedBody(t, data)

	if got["big"] != float64(12345678901234567890) {
		// float64 cannot hold every digit; compare the raw form instead.
		if fmt.Sprint(got["big"]) != "1.2345678901234567e+19" {
			t.Errorf("big = %v", got["big"])
		}
	}
	var round map[string]json.RawMessage
	raw := []byte(data)
	var envelope struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(envelope.Body), &round); err != nil {
		t.Fatal(err)
	}
	if string(round["big"]) != "12345678901234567890" {
		t.Errorf("big int was re-encoded: %s", round["big"])
	}
	if string(round["pi"]) != "3.14159265358979323846" {
		t.Errorf("float was re-encoded: %s", round["pi"])
	}
	if string(round["future"]) != `{"nested":[1,2,{"x":null}]}` {
		t.Errorf("unknown object changed: %s", round["future"])
	}
}

func TestNonChatRequestBodyIsByteIdentical(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	for _, body := range []string{
		`{"z":1,   "a": [1,2]}`,
		`{"model":"m","temperature":1}`,
		`[1,2,3]`,
		"not json at all",
	} {
		_, data := post(t, p.URL+"/api/show", body)
		envelope := decodeMap(t, data)
		if envelope["body"] != body {
			t.Errorf("body changed on /api/show:\n got %q\nwant %q", envelope["body"], body)
		}
	}

	_, data := do(t, http.MethodPut, p.URL+"/api/pull", `{"name":"x"}`, nil)
	if decodeMap(t, data)["body"] != `{"name":"x"}` {
		t.Error("PUT body changed")
	}
}

func TestInvalidChatBodiesPassThrough(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	cases := map[string]string{
		"array":        `[1,2]`,
		"string":       `"hello"`,
		"number":       `42`,
		"null":         `null`,
		"syntax error": `{"model":`,
		"whitespace":   "   ",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, data := post(t, p.URL+"/v1/chat/completions", body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			envelope := decodeMap(t, data)
			if envelope["body"] != body {
				t.Errorf("body changed:\n got %q\nwant %q", envelope["body"], body)
			}
		})
	}
}

func TestGetPassthroughQueryAndHeaders(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	headers := map[string]string{
		"Authorization": "Bearer secret-token",
		"X-Custom":      "custom-value",
		"Connection":    "keep-alive",
	}
	resp, data := do(t, http.MethodGet, p.URL+"/api/tags?x=1&y=a%20b", "", headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Error("upstream response header was not forwarded")
	}

	envelope := decodeMap(t, data)
	if envelope["method"] != "GET" {
		t.Errorf("method = %v", envelope["method"])
	}
	if envelope["uri"] != "/api/tags?x=1&y=a%20b" {
		t.Errorf("uri = %v", envelope["uri"])
	}
	forwarded := envelope["headers"].(map[string]any)
	if got := forwarded["Authorization"]; fmt.Sprint(got) != "[Bearer secret-token]" {
		t.Errorf("Authorization = %v", got)
	}
	if got := forwarded["X-Custom"]; fmt.Sprint(got) != "[custom-value]" {
		t.Errorf("X-Custom = %v", got)
	}
	if _, present := forwarded["Connection"]; present {
		t.Errorf("hop-by-hop Connection header was forwarded: %v", forwarded["Connection"])
	}
}

func TestUpstreamErrorsPassThrough(t *testing.T) {
	for _, status := range []int{400, 404, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":"upstream %d"}`, status)
			})
			p := newProxy(t, up, nil)

			resp, data := post(t, p.URL+"/v1/chat/completions", `{"model":"m"}`)
			if resp.StatusCode != status {
				t.Errorf("status = %d, want %d", resp.StatusCode, status)
			}
			if string(data) != fmt.Sprintf(`{"error":"upstream %d"}`, status) {
				t.Errorf("body = %s", data)
			}
		})
	}
}

func TestUpstreamUnreachableIs502(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {})
	p := newProxy(t, up, nil)
	up.Close()

	resp, data := do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", resp.StatusCode, data)
	}
	body := decodeMap(t, data)
	message, ok := body["error"].(map[string]any)["message"].(string)
	if !ok || !strings.HasPrefix(message, "proxy:") {
		t.Errorf("error body = %s", data)
	}

	// Still failing afterwards, and still answering, not crashing.
	resp, _ = do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status after failure = %d", resp.StatusCode)
	}
}

func TestHead204And304(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/head":
			w.Header().Set("Content-Length", "5")
			w.Header().Set("X-Upstream", "head")
		case "/204":
			w.WriteHeader(http.StatusNoContent)
		case "/304":
			w.WriteHeader(http.StatusNotModified)
		}
	})
	p := newProxy(t, up, nil)

	resp, data := do(t, http.MethodHead, p.URL+"/head", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream") != "head" {
		t.Error("HEAD response header lost")
	}
	if len(data) != 0 {
		t.Errorf("HEAD body = %q, want empty", data)
	}

	for _, path := range []string{"/204", "/304"} {
		resp, data := do(t, http.MethodGet, p.URL+path, "", nil)
		if resp.StatusCode != 204 && resp.StatusCode != 304 {
			t.Errorf("%s status = %d", path, resp.StatusCode)
		}
		if len(data) != 0 {
			t.Errorf("%s body = %q", path, data)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, func(o *Options) { o.MaxBodySize = 16 })

	atLimit := `{"model":"abcd"}` // exactly 16 bytes
	if len(atLimit) != 16 {
		t.Fatalf("test body length = %d", len(atLimit))
	}
	resp, data := post(t, p.URL+"/v1/chat/completions", atLimit)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("at-limit status = %d, body = %s", resp.StatusCode, data)
	}
	if forwardedBody(t, data)["reasoning_effort"] != "none" {
		t.Error("at-limit body was not injected")
	}

	overLimit := `{"model":"abcde"}` // 17 bytes
	resp, data = post(t, p.URL+"/v1/chat/completions", overLimit)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit status = %d, want 413 (body %s)", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "请求体超过上限") {
		t.Errorf("413 body = %s", data)
	}

	// Non-chat paths are never buffered, so no limit applies there.
	resp, data = post(t, p.URL+"/api/show", strings.Repeat("x", 4096))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-chat status = %d", resp.StatusCode)
	}
	if len(decodeMap(t, data)["body"].(string)) != 4096 {
		t.Error("non-chat body was truncated")
	}
}

func TestConcurrencyLimitReturns503(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			started <- struct{}{}
			<-release
			_, _ = io.WriteString(w, "slow done")
			return
		}
		_, _ = io.WriteString(w, "fast")
	})
	p := newProxy(t, up, func(o *Options) {
		o.MaxConcurrent = 1
		o.AcquireTimeout = 100 * time.Millisecond
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := http.Get(p.URL + "/slow")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-started

	resp, data := do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (body %s)", resp.StatusCode, data)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}
	body := decodeMap(t, data)
	if _, ok := body["error"].(map[string]any)["message"]; !ok {
		t.Errorf("503 body = %s", data)
	}

	close(release)
	wg.Wait()

	resp, _ = do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status after release = %d, want 200", resp.StatusCode)
	}
}

func TestClientCancelReleasesSlot(t *testing.T) {
	canceled := make(chan struct{}, 1)
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			<-r.Context().Done()
			canceled <- struct{}{}
			return
		}
		_, _ = io.WriteString(w, "fast")
	})
	p := newProxy(t, up, func(o *Options) {
		o.MaxConcurrent = 1
		o.AcquireTimeout = 2 * time.Second
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/hang", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("expected the canceled request to fail")
	}

	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request was not canceled")
	}

	resp, data := do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("slot not released, status = %d (body %s)", resp.StatusCode, data)
	}
}

func TestStreamingIsNotBuffered(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream has no flusher")
			return
		}
		for _, chunk := range []string{
			"data: {\"token\":\"A\"}\n\n",
			"data: {\"token\":\"B\"}\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, chunk)
			flusher.Flush()
			time.Sleep(200 * time.Millisecond)
		}
	})
	p := newProxy(t, up, nil)

	start := time.Now()
	resp, err := http.Post(p.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	firstAt := time.Since(start)
	if firstLine != "data: {\"token\":\"A\"}\n" {
		t.Errorf("first line = %q", firstLine)
	}

	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	total := time.Since(start)

	if firstAt > 250*time.Millisecond {
		t.Errorf("first chunk arrived after %v; the stream was buffered", firstAt)
	}
	if total < 500*time.Millisecond {
		t.Errorf("total %v, expected the upstream to still be streaming", total)
	}
	if !strings.Contains(string(rest), "[DONE]") {
		t.Errorf("rest = %q", rest)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestConcurrentStreams(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	p := newProxy(t, up, func(o *Options) { o.MaxConcurrent = 8 })

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(p.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if !strings.Contains(string(data), "data: 4") {
				errs <- fmt.Errorf("stream incomplete: %q", data)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestUpstreamDisconnectMidStream(t *testing.T) {
	var healthy atomic.Bool
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"token\":\"A\"}\n\n")
		w.(http.Flusher).Flush()
		// Kill the connection without a clean chunked terminator.
		panic(http.ErrAbortHandler)
	})
	p := newProxy(t, up, nil)

	resp, err := http.Post(p.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body) // may end with an error; must not hang

	// The proxy is still able to serve the next request.
	healthy.Store(true)
	resp2, data := do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	if resp2.StatusCode != http.StatusOK || string(data) != `{"ok":true}` {
		t.Errorf("after disconnect: status %d body %s", resp2.StatusCode, data)
	}
}

type profileSettings struct {
	upstream *url.URL
	timeout  time.Duration
	params   map[string]json.RawMessage
	profiles map[string]policy.Policy
}

func (s profileSettings) UpstreamURL() *url.URL              { return s.upstream }
func (s profileSettings) Timeout() time.Duration             { return s.timeout }
func (s profileSettings) Params() map[string]json.RawMessage { return s.params }
func (s profileSettings) Policy(name string) (policy.Policy, bool) {
	if name == "" {
		return policy.New(s.params), true
	}
	resolved, ok := s.profiles[name]
	return resolved, ok
}

func TestWorkbenchThinkingHeader(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	headers := map[string]string{
		"Content-Type":         "application/json",
		"X-Workbench-Thinking": "passthrough",
	}
	resp, data := do(t, http.MethodPost, p.URL+"/v1/chat/completions",
		`{"model":"m","think":true,"reasoning_effort":"high"}`, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, data)
	}
	got := forwardedBody(t, data)
	if got["think"] != true || got["reasoning_effort"] != "high" {
		t.Errorf("passthrough changed thinking fields: %v", got)
	}
	if got["temperature"] != 0.2 {
		t.Errorf("params should still be injected: %v", got)
	}
	envelope := decodeMap(t, data)
	upstreamHeaders := envelope["headers"].(map[string]any)
	if _, present := upstreamHeaders["X-Workbench-Thinking"]; present {
		t.Error("control header was forwarded upstream")
	}
}

func TestWorkbenchThinkingHeaderOn(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)

	headers := map[string]string{
		"Content-Type":         "application/json",
		"X-Workbench-Thinking": "on",
	}
	_, data := do(t, http.MethodPost, p.URL+"/v1/chat/completions",
		`{"model":"m","think":true,"reasoning_effort":"high"}`, headers)
	got := forwardedBody(t, data)
	if got["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v", got["reasoning_effort"])
	}
	if _, present := got["think"]; present {
		t.Error("think should be removed on /v1 even when thinking is on")
	}
}

func TestWorkbenchProfileHeader(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	base, _ := url.Parse(up.URL)
	settings := profileSettings{
		upstream: base,
		timeout:  5 * time.Second,
		params:   defaultParams(),
		profiles: map[string]policy.Policy{
			"raw": {Thinking: policy.ThinkingPassthrough, Params: map[string]json.RawMessage{}},
		},
	}
	p := newProxy(t, up, func(o *Options) { o.Settings = settings })

	headers := map[string]string{
		"Content-Type":        "application/json",
		"X-Workbench-Profile": "raw",
	}
	_, data := do(t, http.MethodPost, p.URL+"/v1/chat/completions",
		`{"model":"m","think":true,"reasoning_effort":"high","temperature":1}`, headers)
	got := forwardedBody(t, data)
	if got["think"] != true || got["reasoning_effort"] != "high" || got["temperature"] != float64(1) {
		t.Errorf("raw profile changed the body: %v", got)
	}
	if _, present := got["max_tokens"]; present {
		t.Error("raw profile injected params")
	}
	if envelope := decodeMap(t, data); envelope["headers"].(map[string]any)["X-Workbench-Profile"] != nil {
		t.Error("profile header was forwarded upstream")
	}
}

func TestUnknownProfileIsRejected(t *testing.T) {
	called := false
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, "{}")
	})
	p := newProxy(t, up, nil)
	headers := map[string]string{
		"Content-Type":        "application/json",
		"X-Workbench-Profile": "missing",
	}
	resp, data := do(t, http.MethodPost, p.URL+"/v1/chat/completions", `{"model":"m"}`, headers)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", resp.StatusCode, data)
	}
	if called {
		t.Error("unknown profile reached the upstream")
	}
}

func TestInvalidThinkingHeaderIsRejected(t *testing.T) {
	up := newUpstream(t, echoHandler(t))
	p := newProxy(t, up, nil)
	headers := map[string]string{
		"Content-Type":         "application/json",
		"X-Workbench-Thinking": "maybe",
	}
	resp, data := do(t, http.MethodPost, p.URL+"/v1/chat/completions", `{"model":"m"}`, headers)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", resp.StatusCode, data)
	}
}

type captureRecorder struct {
	ch chan inspect.Capture
}

func newCaptureRecorder() *captureRecorder {
	return &captureRecorder{ch: make(chan inspect.Capture, 4)}
}

func (r *captureRecorder) Record(capture inspect.Capture) { r.ch <- capture }

func (r *captureRecorder) wait(t *testing.T) inspect.Capture {
	t.Helper()
	select {
	case capture := <-r.ch:
		return capture
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not arrive")
		return inspect.Capture{}
	}
}

func TestCaptureNonStream(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	recorder := newCaptureRecorder()
	p := newProxy(t, up, func(o *Options) { o.CaptureRecorder = recorder })

	_, _ = post(t, p.URL+"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	capture := recorder.wait(t)
	if capture.ID == "" || capture.Method != "POST" || capture.Path != "/v1/chat/completions" {
		t.Errorf("capture = %+v", capture)
	}
	if capture.Source != "proxy" || capture.Provider != "ollama" {
		t.Errorf("source/provider = %q/%q", capture.Source, capture.Provider)
	}
	if !strings.Contains(string(capture.RequestOriginal), `"model":"m"`) {
		t.Errorf("original = %s", capture.RequestOriginal)
	}
	if !strings.Contains(string(capture.RequestForwarded), `"reasoning_effort":"none"`) {
		t.Errorf("forwarded = %s", capture.RequestForwarded)
	}
	if capture.Status != 200 || !strings.Contains(string(capture.ResponseBody), `"ok":true`) {
		t.Errorf("status/body = %d / %s", capture.Status, capture.ResponseBody)
	}
	if capture.Duration <= 0 || capture.TTFT <= 0 {
		t.Errorf("timings = %v/%v", capture.Duration, capture.TTFT)
	}
	if capture.Stream {
		t.Error("non-stream response marked as stream")
	}
}

func TestCaptureStream(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{"data: {\"a\":1}\n\n", "data: [DONE]\n\n"} {
			_, _ = io.WriteString(w, chunk)
			flusher.Flush()
		}
	})
	recorder := newCaptureRecorder()
	p := newProxy(t, up, func(o *Options) { o.CaptureRecorder = recorder })

	_, _ = post(t, p.URL+"/v1/chat/completions", `{"model":"m"}`)
	capture := recorder.wait(t)
	if !capture.Stream {
		t.Error("stream response not marked")
	}
	if !strings.Contains(string(capture.ResponseBody), "[DONE]") {
		t.Errorf("stream body = %q", capture.ResponseBody)
	}
}

func TestCaptureSkipsNonChatAndTruncates(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	})
	recorder := newCaptureRecorder()
	p := newProxy(t, up, func(o *Options) {
		o.CaptureRecorder = recorder
		o.CaptureBodyLimit = 10
	})

	_, _ = do(t, http.MethodGet, p.URL+"/api/tags", "", nil)
	select {
	case capture := <-recorder.ch:
		t.Fatalf("non-chat request was captured: %+v", capture)
	case <-time.After(100 * time.Millisecond):
	}

	_, _ = post(t, p.URL+"/v1/chat/completions", `{"model":"m"}`)
	capture := recorder.wait(t)
	if !capture.ResponseTruncated {
		t.Error("response truncation flag missing")
	}
	if len(capture.ResponseBody) != 10 {
		t.Errorf("body length = %d", len(capture.ResponseBody))
	}
}
