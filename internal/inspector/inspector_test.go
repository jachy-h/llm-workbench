package inspector

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/storage"
)

func openInspector(t *testing.T, captureBodies bool, maxBytes int) (*Inspector, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	insp, err := New(Options{
		Store:         store,
		Enabled:       true,
		CaptureBodies: captureBodies,
		MaxBodyBytes:  maxBytes,
		QueueSize:     16,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = insp.Close(context.Background()) })
	return insp, store
}

func sampleCapture() inspect.Capture {
	return inspect.Capture{
		ID:               "req_test",
		Started:          time.Now().UTC(),
		Source:           "proxy",
		Provider:         "ollama",
		Method:           "POST",
		Path:             "/v1/chat/completions",
		Client:           "127.0.0.1",
		Note:             "injected:v1",
		Status:           200,
		Duration:         1200 * time.Millisecond,
		TTFT:             200 * time.Millisecond,
		RequestOriginal:  []byte(`{"model":"qwen3:8b","api_key":"sk-secret","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hi"}]}`),
		RequestForwarded: []byte(`{"model":"qwen3:8b","api_key":"sk-secret","reasoning_effort":"none","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hi"}]}`),
		ResponseBody:     []byte(`{"model":"qwen3:8b","message":{"content":"hello"},"done":true,"prompt_eval_count":10,"eval_count":20}`),
	}
}

func TestRecordPersistsAndRedacts(t *testing.T) {
	insp, store := openInspector(t, true, 1<<20)
	insp.Record(sampleCapture())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := insp.Close(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if record.Model != "qwen3:8b" || record.SystemPrompt != "be terse" {
		t.Errorf("record = %+v", record)
	}
	if record.PromptTokens == nil || *record.PromptTokens != 10 || record.CompletionTokens == nil || *record.CompletionTokens != 20 {
		t.Errorf("usage = %+v", record)
	}
	if !strings.Contains(record.RequestForwarded, `"api_key":"[REDACTED]"`) {
		t.Errorf("api_key was not redacted: %s", record.RequestForwarded)
	}
	if !strings.Contains(record.RequestForwarded, `"messages"`) {
		t.Errorf("body was lost: %s", record.RequestForwarded)
	}
	if record.StatusCode != 200 || record.DurationMS != 1200 || record.TTFTMS != 200 {
		t.Errorf("metadata = %+v", record)
	}
}

func TestRecordWithoutBodies(t *testing.T) {
	insp, store := openInspector(t, false, 1<<20)
	insp.Record(sampleCapture())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := insp.Close(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if record.RequestOriginal != "" || record.RequestForwarded != "" || record.ResponseBody != "" {
		t.Errorf("bodies persisted with capture disabled: %+v", record)
	}
	if record.Model != "qwen3:8b" || record.SystemPrompt != "be terse" {
		t.Errorf("metadata missing: %+v", record)
	}
}

func TestRecordAfterCloseDoesNotPanic(t *testing.T) {
	insp, _ := openInspector(t, true, 1<<20)
	if err := insp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	insp.Record(sampleCapture())
}

func TestRedactJSON(t *testing.T) {
	body := []byte(`{
		"api_key":"sk",
		"authorization":"Bearer x",
		"max_tokens":96,
		"nested":{"token":"abc","temperature":0.2,"list":[{"password":"p"}]},
		"messages":[{"content":"keep me","tool_calls":[{"api_key":"x"}]}]
	}`)
	out := string(RedactJSON(body))
	for _, secret := range []string{"sk", "Bearer x", `"abc"`, `"password":"p"`} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q survived: %s", secret, out)
		}
	}
	if !strings.Contains(out, `"max_tokens":96`) {
		t.Errorf("max_tokens was redacted: %s", out)
	}
	if !strings.Contains(out, `"temperature":0.2`) || !strings.Contains(out, "keep me") {
		t.Errorf("non-sensitive data changed: %s", out)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("redacted JSON is invalid: %s", out)
	}
}

func TestRedactJSONNonJSON(t *testing.T) {
	for _, input := range []string{"not json", `"text"`, `42`} {
		if got := string(RedactJSON([]byte(input))); got != input {
			t.Errorf("RedactJSON(%q) = %q", input, got)
		}
	}
}

func TestRedactURL(t *testing.T) {
	got := RedactURL("http://user:pass@example.com/api?api_key=sk&x=1&token=abc")
	if strings.Contains(got, "sk") || strings.Contains(got, "abc") || strings.Contains(got, "user") {
		t.Errorf("RedactURL = %s", got)
	}
	if !strings.Contains(got, "x=1") {
		t.Errorf("non-sensitive query was changed: %s", got)
	}
}

func TestExtract(t *testing.T) {
	capture := sampleCapture()
	capture.Stream = true
	info := Extract(capture)
	if info.Model != "qwen3:8b" || !info.Stream || info.SystemPrompt != "be terse" {
		t.Errorf("info = %+v", info)
	}
	// Request body stream:true should also be detected.
	capture.Stream = false
	capture.RequestForwarded = []byte(`{"model":"m","stream":true,"messages":[]}`)
	if !Extract(capture).Stream {
		t.Error("stream:true was not detected")
	}
}

func TestExtractUsageDirect(t *testing.T) {
	openAI := []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	usage := extractUsage(openAI)
	if usage == nil || *usage.TotalTokens != 7 {
		t.Errorf("openai usage = %+v", usage)
	}
	ollama := []byte(`{"prompt_eval_count":5,"eval_count":6}`)
	usage = extractUsage(ollama)
	if usage == nil || *usage.TotalTokens != 11 {
		t.Errorf("ollama usage = %+v", usage)
	}
}

func TestExtractUsageStream(t *testing.T) {
	body := []byte("data: {\"message\":{\"content\":\"hi\"}}\n\n" +
		"data: {\"done\":true,\"prompt_eval_count\":2,\"eval_count\":3}\n\n" +
		"data: [DONE]\n\n")
	usage := extractUsage(body)
	if usage == nil || *usage.PromptTokens != 2 || *usage.CompletionTokens != 3 || *usage.TotalTokens != 5 {
		t.Errorf("usage = %s", usage)
	}
	ndjson := []byte("{\"message\":{\"content\":\"a\"}}\n{\"done\":true,\"prompt_eval_count\":1,\"eval_count\":1}\n")
	usage = extractUsage(ndjson)
	if usage == nil || *usage.TotalTokens != 2 {
		t.Errorf("ndjson usage = %s", usage)
	}
	frames := []byte(`[{"message":{"content":"a"}},{"done":true,"prompt_eval_count":3,"eval_count":4}]`)
	usage = extractUsage(frames)
	if usage == nil || *usage.TotalTokens != 7 {
		t.Errorf("frame array usage = %s", usage)
	}
}

func TestTruncation(t *testing.T) {
	insp, store := openInspector(t, true, 40)
	insp.Record(sampleCapture())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := insp.Close(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if !record.ResponseTruncated {
		t.Error("truncated flag missing")
	}
	if len(record.RequestForwarded) > 60 || !strings.Contains(record.RequestForwarded, "truncated") {
		t.Errorf("body not truncated: %q", record.RequestForwarded)
	}
}

func TestMaintain(t *testing.T) {
	insp, store := openInspector(t, false, 1<<20)
	old := time.Now().Add(-48 * time.Hour)
	if err := store.Insert(context.Background(), storage.Record{
		ID: "old", CreatedAt: old, Source: "proxy", Provider: "ollama", Model: "m", Endpoint: "/v1/chat/completions", Method: "POST",
	}); err != nil {
		t.Fatal(err)
	}
	insp.opts.RetentionDays = 1
	if _, err := insp.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "old"); err != storage.ErrNotFound {
		t.Errorf("old record survived: %v", err)
	}
}

func TestDisabledInspector(t *testing.T) {
	insp, err := New(Options{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	insp.Record(sampleCapture())
	if err := insp.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if insp.Written() != 0 || insp.Dropped() != 0 {
		t.Errorf("written=%d dropped=%d", insp.Written(), insp.Dropped())
	}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Options{Enabled: true}); err == nil {
		t.Error("enabled inspector without store was accepted")
	}
}
