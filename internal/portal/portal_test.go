package portal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/config"
	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/logstore"
	"github.com/jachy-h/llm-workbench/internal/policy"
)

type fixture struct {
	handler  *Handler
	cfg      *config.Config
	logs     *logstore.Log
	stopping *atomic.Bool
	stopOnce sync.Once
	stopped  chan struct{}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]string{
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
		"--db", filepath.Join(dir, "workbench.db"),
	}, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		cfg:      cfg,
		logs:     logstore.New(cfg.LogPath, logstore.DefaultCapacity),
		stopping: &atomic.Bool{},
		stopped:  make(chan struct{}),
	}
	f.handler = New(Options{
		Version:  "test-version",
		PID:      4242,
		Listen:   "http://127.0.0.1:11500",
		Config:   cfg,
		Log:      f.logs,
		Defaults: DefaultsFor(),
		LaunchCommand: func() []string {
			return []string{"/usr/local/bin/llm-workbench", "--foreground"}
		},
		Workdir:  "/tmp",
		Stopping: f.stopping,
		Stop: func() {
			f.stopOnce.Do(func() { close(f.stopped) })
		},
	})
	t.Cleanup(func() { _ = f.logs.Close() })
	return f
}

func (f *fixture) do(method, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:11500"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	if !f.handler.Handle(rec, req) {
		http.NotFound(rec, req)
	}
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestPIDEndpoint(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/_/llm-proxy/api/pid", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.PID != 4242 {
		t.Errorf("pid = %d, want 4242", out.PID)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

func TestLoopbackOnly(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/_/llm-proxy/api/state", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	rec := httptest.NewRecorder()
	if !f.handler.Handle(rec, req) {
		t.Fatal("portal request was not handled")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "只允许本机访问") {
		t.Errorf("body = %s", rec.Body.String())
	}

	req.RemoteAddr = "[::1]:5555"
	req.Host = "[::1]:11500"
	rec = httptest.NewRecorder()
	f.handler.Handle(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("IPv6 loopback status = %d", rec.Code)
	}
}

func TestNonPortalPathsAreNotHandled(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	if f.handler.Handle(rec, req) {
		t.Fatal("Handle reported /api/tags as portal")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("Handle wrote %q for a non-portal path", rec.Body.String())
	}
}

func TestStateShape(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/_/llm-proxy/api/state", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	state := decodeJSON(t, rec)

	for _, key := range []string{
		"version", "pid", "listen", "upstream", "timeout", "log_path",
		"log_capacity", "config_path", "launch_ready", "stopping", "defaults", "autostart",
		"thinking", "inspector", "storage",
		"max_concurrent", "max_body_size",
	} {
		if _, ok := state[key]; !ok {
			t.Errorf("state missing %q: %v", key, state)
		}
	}
	for _, key := range []string{"params", "system_prompt"} {
		if _, ok := state[key]; ok {
			t.Errorf("state still exposes %q: %v", key, state)
		}
	}
	if state["version"] != "test-version" || state["pid"] != float64(4242) {
		t.Errorf("version/pid = %v/%v", state["version"], state["pid"])
	}
	if state["log_capacity"] != float64(100) {
		t.Errorf("log_capacity = %v", state["log_capacity"])
	}
	if state["launch_ready"] != true {
		t.Errorf("launch_ready = %v", state["launch_ready"])
	}
	if state["stopping"] != false {
		t.Errorf("stopping = %v", state["stopping"])
	}
	defaults := state["defaults"].(map[string]any)
	if defaults["upstream"] != config.DefaultUpstream {
		t.Errorf("defaults.upstream = %v", defaults["upstream"])
	}
	if _, ok := defaults["params"]; ok {
		t.Errorf("defaults still expose params: %v", defaults)
	}
	if defaults["max_concurrent"] != float64(config.DefaultMaxConcurrent) {
		t.Errorf("defaults.max_concurrent = %v", defaults["max_concurrent"])
	}
	if state["max_concurrent"] != float64(config.DefaultMaxConcurrent) {
		t.Errorf("max_concurrent = %v", state["max_concurrent"])
	}
	if state["thinking"] != "off" {
		t.Errorf("thinking = %v", state["thinking"])
	}
	autostart := state["autostart"].(map[string]any)
	if autostart["label"] != "com.github.llm-workbench" {
		t.Errorf("autostart.label = %v", autostart["label"])
	}
	if autostart["supported"] != (runtime.GOOS == "darwin") {
		t.Errorf("autostart.supported = %v", autostart["supported"])
	}
}

func TestConfigSaveAppliesAndPersists(t *testing.T) {
	f := newFixture(t)
	payload := `{"upstream":"http://127.0.0.1:11435/","timeout":30,` +
		`"max_concurrent":3,"max_body_size":2048,"thinking":"on",` +
		`"inspector":{"capture_bodies":true,"retention_days":2,"max_rows":7}}`
	rec := f.do(http.MethodPost, "/_/llm-proxy/api/config", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	state := decodeJSON(t, rec)
	if state["upstream"] != "http://127.0.0.1:11435" {
		t.Errorf("upstream = %v", state["upstream"])
	}
	if state["timeout"] != float64(30) {
		t.Errorf("timeout = %v", state["timeout"])
	}
	if state["thinking"] != "on" {
		t.Errorf("thinking = %v", state["thinking"])
	}
	if state["max_concurrent"] != float64(3) || state["max_body_size"] != float64(2048) {
		t.Errorf("limits = %v/%v", state["max_concurrent"], state["max_body_size"])
	}
	if f.cfg.Upstream() != "http://127.0.0.1:11435" || f.cfg.Timeout() != 30*time.Second {
		t.Errorf("live config = %+v", f.cfg.Snapshot())
	}
	if f.cfg.MaxConcurrent() != 3 || f.cfg.MaxBodySize() != 2048 {
		t.Errorf("live limits = %d/%d", f.cfg.MaxConcurrent(), f.cfg.MaxBodySize())
	}
	resolved := f.cfg.Policy()
	if resolved.Thinking != policy.ThinkingOn {
		t.Errorf("live policy = %+v", resolved)
	}

	data, err := os.ReadFile(f.cfg.ConfigPath)
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("config JSON: %v", err)
	}
	if document["upstream"] != "http://127.0.0.1:11435" || document["timeout"] != float64(30) {
		t.Errorf("saved document = %v", document)
	}
}

func TestConfigRejectsInvalidPayloads(t *testing.T) {
	f := newFixture(t)
	cases := map[string]int{
		`{"upstream":"ftp://x"}`:  400,
		`{"timeout":0}`:           400,
		`{"max_concurrent":0}`:    400,
		`{"max_body_size":0}`:     400,
		`{"profiles":{"raw":{}}}`: 400,
		`{"profile":"raw"}`:       400,
		`not json`:                400,
		`[1,2]`:                   400,
		`null`:                    400,
	}
	for payload, want := range cases {
		rec := f.do(http.MethodPost, "/_/llm-proxy/api/config", payload)
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d (%s)", payload, rec.Code, want, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("%s: body = %s", payload, rec.Body.String())
		}
	}
	if f.cfg.Upstream() != config.DefaultUpstream {
		t.Errorf("rejected payloads changed upstream: %q", f.cfg.Upstream())
	}
}

func TestConfigOversizedBody(t *testing.T) {
	f := newFixture(t)
	payload := `{"future":"` + strings.Repeat("a", MaxBody) + `"}`
	rec := f.do(http.MethodPost, "/_/llm-proxy/api/config", payload)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestLogEntriesIncremental(t *testing.T) {
	f := newFixture(t)
	first := f.logs.WriteRequest("POST", "/v1/chat/completions", 200, "injected:v1", "127.0.0.1", "")

	rec := f.do(http.MethodGet, "/_/llm-proxy/api/log?since=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	entries := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	entry := entries[0].(map[string]any)
	if entry["seq"] != float64(first.Seq) || entry["method"] != "POST" || entry["status"] != float64(200) {
		t.Errorf("entry = %v", entry)
	}
	if entry["note"] != "injected:v1" || entry["level"] != "ok" {
		t.Errorf("entry = %v", entry)
	}
	if body["capacity"] != float64(100) || body["poll_ms"] != float64(PollMS) {
		t.Errorf("capacity/poll = %v/%v", body["capacity"], body["poll_ms"])
	}
	if body["path"] != f.logs.Path() {
		t.Errorf("path = %v", body["path"])
	}
	if !strings.Contains(body["text"].(string), "injected:v1") {
		t.Errorf("text = %v", body["text"])
	}

	again := decodeJSON(t, f.do(http.MethodGet, fmt.Sprintf("/_/llm-proxy/api/log?since=%d", first.Seq), ""))
	if len(again["entries"].([]any)) != 0 {
		t.Errorf("incremental poll returned duplicates: %v", again["entries"])
	}

	f.logs.Write("second line", "", "")
	tail := decodeJSON(t, f.do(http.MethodGet, "/_/llm-proxy/api/log?lines=1", ""))
	if got := len(tail["entries"].([]any)); got != 1 {
		t.Errorf("lines=1 returned %d entries", got)
	}
}

func TestShutdownStopsAsync(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodPost, "/_/llm-proxy/api/shutdown", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	if body["ok"] != true {
		t.Errorf("body = %v", body)
	}
	select {
	case <-f.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop callback was not invoked")
	}
	if !f.stopping.Load() {
		t.Error("stopping flag not set")
	}

	again := decodeJSON(t, f.do(http.MethodPost, "/_/llm-proxy/api/shutdown", ""))
	if again["message"] != "服务已经在关闭中" {
		t.Errorf("second shutdown = %v", again)
	}
}

func TestAutostartRouteUsesCurrentState(t *testing.T) {
	f := newFixture(t)
	// DELETE is safe on non-darwin (returns unsupported), and on darwin it
	// only touches a plist inside the user's LaunchAgents. Tests must never
	// call POST here: it would install a real login item.
	if runtime.GOOS == "darwin" {
		t.Skip("refusing to modify the real LaunchAgents directory")
	}
	rec := f.do(http.MethodDelete, "/_/llm-proxy/api/autostart", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	if body["ok"] != false || !strings.Contains(body["message"].(string), "macOS") {
		t.Errorf("body = %v", body)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/_/llm-proxy/api/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "未找到") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// TestLanguageReachesState covers the server half of the switch: the state
// reports the language, and saving it changes both state and runtime language.
func TestLanguageReachesState(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	f := newFixture(t)

	if got := decodeJSON(t, f.do(http.MethodGet, "/_/llm-proxy/api/state", ""))["lang"]; got != "zh" {
		t.Errorf("state lang = %v, want zh", got)
	}

	rec := f.do(http.MethodPost, "/_/llm-proxy/api/config", `{"lang":"en"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("config status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeJSON(t, rec)["lang"]; got != "en" {
		t.Errorf("saved lang = %v, want en", got)
	}
	if got := i18n.Current(); got != i18n.EN {
		t.Errorf("runtime language = %q, want en", got)
	}

	// A bad value is rejected and leaves the language alone.
	if rec := f.do(http.MethodPost, "/_/llm-proxy/api/config", `{"lang":"fr"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("lang=fr status = %d, want 400", rec.Code)
	}
	if got := i18n.Current(); got != i18n.EN {
		t.Errorf("runtime language after a rejected value = %q", got)
	}
}
