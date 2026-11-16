package workbench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/engine"
	"github.com/jachy-h/llm-workbench/internal/experiments"
	"github.com/jachy-h/llm-workbench/internal/inspector"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/storage"
)

func newTestHandler(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Now().UTC()
	records := []storage.Record{
		{ID: "req_a", CreatedAt: now.Add(-time.Minute), Source: "proxy", Provider: "ollama", Model: "qwen3:8b", Endpoint: "/v1/chat/completions", Method: "POST", Stream: true, StatusCode: 200, DurationMS: 100, TTFTMS: 20, SystemPrompt: "be terse", RequestOriginal: `{"model":"qwen3:8b"}`, RequestForwarded: `{"model":"qwen3:8b","reasoning_effort":"none"}`, ResponseBody: `data: [DONE]`},
		{ID: "req_b", CreatedAt: now, Source: "proxy", Provider: "ollama", Model: "llama3:8b", Endpoint: "/api/chat", Method: "POST", StatusCode: 500, Error: "boom", DurationMS: 200, TTFTMS: 30},
	}
	for _, record := range records {
		if err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	return New(Options{Version: "test", Store: store}), store
}

func call(handler *Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "127.0.0.1:11500"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if !handler.Handle(rec, req) {
		http.NotFound(rec, req)
	}
	return rec
}

func TestServesUI(t *testing.T) {
	handler, _ := newTestHandler(t)
	rec := call(handler, http.MethodGet, "/_/workbench/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Errorf("index page missing root: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("cache-control = %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/_/workbench", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "127.0.0.1:11500"
	rec = httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusTemporaryRedirect {
		t.Errorf("redirect status = %d", rec.Code)
	}
}

func TestServeIndexForPortalRoute(t *testing.T) {
	handler, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/_/llm-proxy", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "127.0.0.1:11500"
	rec := httptest.NewRecorder()
	handler.ServeIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Errorf("index page missing root: %s", rec.Body.String())
	}
}

func TestHealthAndListAndDetail(t *testing.T) {
	handler, _ := newTestHandler(t)
	rec := call(handler, http.MethodGet, "/_/workbench/api/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d: %s", rec.Code, rec.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	storageState := health["storage"].(map[string]any)
	if storageState["count"] != float64(2) {
		t.Errorf("health storage = %v", storageState)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/requests?limit=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items  []map[string]any `json:"items"`
		Total  int64            `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || len(list.Items) != 1 || list.Items[0]["id"] != "req_b" {
		t.Errorf("list = %+v", list)
	}
	if _, ok := list.Items[0]["request_original"]; ok {
		t.Error("list response leaked bodies")
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/requests?provider=ollama&status=500", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filter status = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Items[0]["id"] != "req_b" {
		t.Errorf("filter = %+v", list)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/requests/req_a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", rec.Code, rec.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["request_forwarded"] == "" || detail["response_body"] == "" {
		t.Errorf("detail bodies missing: %v", detail)
	}
	if detail["system_prompt"] != "be terse" {
		t.Errorf("system prompt = %v", detail["system_prompt"])
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/requests/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing status = %d", rec.Code)
	}
}

func TestExportFormats(t *testing.T) {
	handler, _ := newTestHandler(t)
	rec := call(handler, http.MethodGet, "/_/workbench/api/requests/export?format=jsonl", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"req_a"`) {
		t.Errorf("jsonl = %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Errorf("jsonl content-type = %q", ct)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/requests/export?format=csv", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id,created_at") {
		t.Errorf("csv = %d %s", rec.Code, rec.Body.String())
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/requests/req_a/export", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "reasoning_effort") {
		t.Errorf("single export = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDelete(t *testing.T) {
	handler, store := newTestHandler(t)
	rec := call(handler, http.MethodDelete, "/_/workbench/api/requests/req_a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.Get(context.Background(), "req_a"); err != storage.ErrNotFound {
		t.Errorf("record survived: %v", err)
	}
	rec = call(handler, http.MethodDelete, "/_/workbench/api/requests/req_a", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d", rec.Code)
	}
	// A filtered delete is allowed, but a blanket one needs all=true.
	rec = call(handler, http.MethodDelete, "/_/workbench/api/requests", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blanket delete status = %d", rec.Code)
	}
	rec = call(handler, http.MethodDelete, "/_/workbench/api/requests?all=true", "")
	if rec.Code != http.StatusOK {
		t.Errorf("all delete status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRejectsHostAndOrigin(t *testing.T) {
	handler, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/_/workbench/api/health", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("bad host status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/_/workbench/api/requests", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "127.0.0.1:11500"
	req.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("bad origin status = %d", rec.Code)
	}
}

func TestUnknownAPIAndMethod(t *testing.T) {
	handler, _ := newTestHandler(t)
	rec := call(handler, http.MethodGet, "/_/workbench/api/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown API status = %d", rec.Code)
	}
	rec = call(handler, http.MethodPost, "/_/workbench/api/requests/req_a", "{}")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("bad method status = %d", rec.Code)
	}
}

func TestNilStoreHealthStillWorks(t *testing.T) {
	handler := New(Options{Version: "test"})
	rec := call(handler, http.MethodGet, "/_/workbench/api/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(handler, http.MethodGet, "/_/workbench/api/requests", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("requests status = %d", rec.Code)
	}
}

type fakeProvider struct {
	last     provider.ChatRequest
	response provider.ChatResponse
	stream   []provider.StreamEvent
}

func (f *fakeProvider) ID() string   { return "fake" }
func (f *fakeProvider) Name() string { return "Fake" }
func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Streaming: true, Thinking: provider.ThinkingBool, Usage: true, SystemRole: true}
}
func (f *fakeProvider) ListModels(context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "m", Name: "m"}}, nil
}
func (f *fakeProvider) Chat(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	f.last = req
	return f.response, nil
}
func (f *fakeProvider) ChatStream(_ context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) (provider.ChatResponse, error) {
	f.last = req
	for _, event := range f.stream {
		if err := emit(event); err != nil {
			return provider.ChatResponse{}, err
		}
	}
	return f.response, nil
}

func newChatHandler(t *testing.T) (*Handler, *fakeProvider, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fake := &fakeProvider{response: provider.ChatResponse{
		Model: "m", Text: "hello", Thinking: "hmm", FinishReason: "stop",
		Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Raw:   json.RawMessage(`{"ok":true}`),
	}}
	eng, err := engine.New(map[string]provider.Provider{"fake": fake}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Version: "test", Store: store, Engine: eng}), fake, store
}

func TestProvidersAndModels(t *testing.T) {
	handler, _, _ := newChatHandler(t)
	rec := call(handler, http.MethodGet, "/_/workbench/api/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("providers status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items   []map[string]any `json:"items"`
		Default string           `json:"default"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0]["id"] != "fake" || payload.Default != "fake" {
		t.Errorf("providers = %+v", payload)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/providers/fake/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"m"`) {
		t.Errorf("models = %s", rec.Body.String())
	}
	rec = call(handler, http.MethodGet, "/_/workbench/api/providers/nope/models", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown provider status = %d", rec.Code)
	}
}

func TestChatNonStream(t *testing.T) {
	handler, fake, _ := newChatHandler(t)
	body := `{"model":"m","system":"be terse","messages":[{"role":"user","content":"hi"}],"params":{"temperature":0.2},"think":false}`
	rec := call(handler, http.MethodPost, "/_/workbench/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["text"] != "hello" || response["request_id"] == "" {
		t.Errorf("response = %v", response)
	}
	if fake.last.Model != "m" || len(fake.last.Messages) != 2 || fake.last.Messages[0].Role != "system" {
		t.Errorf("prepared request = %+v", fake.last)
	}
	if fake.last.Think == nil || *fake.last.Think != false {
		t.Errorf("think = %v", fake.last.Think)
	}
}

func TestChatStreamSSE(t *testing.T) {
	handler, fake, _ := newChatHandler(t)
	fake.stream = []provider.StreamEvent{
		{Type: provider.EventDelta, Text: "he"},
		{Type: provider.EventDelta, Text: "llo"},
		{Type: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}},
		{Type: provider.EventDone, Done: true},
	}
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"think":true}`
	rec := call(handler, http.MethodPost, "/_/workbench/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("stream status = %d: %s", rec.Code, rec.Body.String())
	}
	output := rec.Body.String()
	for _, needle := range []string{"event: meta", "event: delta", `"text":"he"`, "event: usage", "event: done"} {
		if !strings.Contains(output, needle) {
			t.Errorf("SSE missing %q:\n%s", needle, output)
		}
	}
	if fake.last.Think == nil || *fake.last.Think != true {
		t.Errorf("think = %v", fake.last.Think)
	}
}

func TestPromptAPI(t *testing.T) {
	handler, _, _ := newChatHandler(t)
	body := `{"name":"terse","system":"be terse","messages":[{"role":"user","content":"hi"}],"params":{"temperature":0.1}}`
	rec := call(handler, http.MethodPost, "/_/workbench/api/prompts", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var prompt map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &prompt); err != nil {
		t.Fatal(err)
	}
	id, _ := prompt["id"].(string)
	if !strings.HasPrefix(id, "prm_") || prompt["name"] != "terse" {
		t.Errorf("prompt = %v", prompt)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/prompts", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "terse") {
		t.Errorf("list = %d %s", rec.Code, rec.Body.String())
	}
	update := `{"name":"updated","system":"x","messages":[],"params":{}}`
	rec = call(handler, http.MethodPut, "/_/workbench/api/prompts/"+id, update)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "updated") {
		t.Errorf("update = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(handler, http.MethodDelete, "/_/workbench/api/prompts/"+id, "")
	if rec.Code != http.StatusOK {
		t.Errorf("delete = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(handler, http.MethodGet, "/_/workbench/api/prompts/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d", rec.Code)
	}
}

func TestExperimentAPI(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fake := &fakeProvider{response: provider.ChatResponse{
		Model: "m", Text: "answer", Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}}
	eng, err := engine.New(map[string]provider.Provider{"fake": fake}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	runner := experiments.New(store, eng, nil, 1)
	handler := New(Options{Version: "test", Store: store, Engine: eng, Runner: runner})

	body := `{"name":"A/B","notes":"test","variants":[
		{"provider":"fake","model":"m1","system":"short","params":{"temperature":0.1}},
		{"provider":"fake","model":"m2","system":"long","params":{"temperature":0.7}}],
		"cases":[{"name":"greeting","messages":[{"role":"user","content":"hi"}]},
		{"name":"code","messages":[{"role":"user","content":"code"}]}]}`
	rec := call(handler, http.MethodPost, "/_/workbench/api/experiments", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var experiment storage.Experiment
	if err := json.Unmarshal(rec.Body.Bytes(), &experiment); err != nil {
		t.Fatal(err)
	}
	if len(experiment.Variants) != 2 || len(experiment.Cases) != 2 {
		t.Fatalf("experiment = %+v", experiment)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/experiments", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "A/B") {
		t.Errorf("list = %d %s", rec.Code, rec.Body.String())
	}

	rec = call(handler, http.MethodPost, "/_/workbench/api/experiments/"+experiment.ID+"/run", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run = %d %s", rec.Code, rec.Body.String())
	}
	var start struct {
		BatchID string   `json:"batch_id"`
		RunIDs  []string `json:"run_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if len(start.RunIDs) != 4 || start.BatchID == "" {
		t.Errorf("start = %+v", start)
	}

	deadline := time.Now().Add(5 * time.Second)
	var runs struct {
		Items  []storage.Run `json:"items"`
		Active bool          `json:"active"`
	}
	for {
		rec = call(handler, http.MethodGet, "/_/workbench/api/experiments/"+experiment.ID+"/runs?batch="+start.BatchID, "")
		if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
			t.Fatal(err)
		}
		if len(runs.Items) == 4 && !runs.Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs not finished: %s", rec.Body.String())
		}
		time.Sleep(30 * time.Millisecond)
	}
	for _, run := range runs.Items {
		if run.Status != storage.RunSucceeded || run.Output != "answer" {
			t.Errorf("run = %+v", run)
		}
	}

	runID := runs.Items[0].ID
	rec = call(handler, http.MethodPatch, "/_/workbench/api/runs/"+runID, `{"rating":4,"note":"good"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rating":4`) {
		t.Errorf("rating = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(handler, http.MethodPatch, "/_/workbench/api/runs/"+runID, `{"rating":9}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad rating status = %d", rec.Code)
	}

	rec = call(handler, http.MethodGet, "/_/workbench/api/experiments/"+experiment.ID+"/export?format=csv", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run_id") {
		t.Errorf("csv export = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(handler, http.MethodGet, "/_/workbench/api/experiments/"+experiment.ID+"/export?format=json", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"runs"`) {
		t.Errorf("json export = %d %s", rec.Code, rec.Body.String())
	}

	rec = call(handler, http.MethodDelete, "/_/workbench/api/experiments/"+experiment.ID, "")
	if rec.Code != http.StatusOK {
		t.Errorf("delete = %d %s", rec.Code, rec.Body.String())
	}
	runner.Shutdown()
}

func TestChatValidation(t *testing.T) {
	handler, _, _ := newChatHandler(t)
	for _, body := range []string{
		`{}`,
		`{"model":"m"}`,
		`{"messages":[{"role":"user","content":"hi"}]}`,
		`not json`,
	} {
		rec := call(handler, http.MethodPost, "/_/workbench/api/chat", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q status = %d", body, rec.Code)
		}
	}
}

func TestPlaygroundCapture(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fake := &fakeProvider{response: provider.ChatResponse{
		Model: "m", Text: "hello", Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Raw: json.RawMessage(`{"message":{"content":"hello"},"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`),
	}}
	eng, err := engine.New(map[string]provider.Provider{"fake": fake}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	// Global body capture is off; the playground capture must still persist its
	// own bodies.
	insp, err := inspector.New(inspector.Options{Store: store, Enabled: true, CaptureBodies: false})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(Options{Version: "test", Store: store, Engine: eng, Inspector: insp})
	rec := call(handler, http.MethodPost, "/_/workbench/api/chat",
		`{"model":"m","system":"be terse","messages":[{"role":"user","content":"hi"}],"think":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if err := insp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), response.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Source != "playground" || record.Model != "m" || record.SystemPrompt != "be terse" {
		t.Errorf("record = %+v", record)
	}
	if record.RequestForwarded == "" || record.ResponseBody == "" {
		t.Errorf("playground bodies missing: %+v", record)
	}
	if record.PromptTokens == nil || *record.PromptTokens != 1 {
		t.Errorf("usage = %+v", record.PromptTokens)
	}
}
