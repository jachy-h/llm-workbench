package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/provider"
)

func newProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(base, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListModels(t *testing.T) {
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"qwen3:8b","model":"qwen3:8b","size":123,"digest":"abc","details":{"family":"qwen3","parameter_size":"8B","quantization_level":"Q4_K_M"}},
			{"name":"llama3:8b","model":"llama3:8b","size":456,"digest":"def","details":{"family":"llama"}}
		]}`)
	})
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
	if models[0].Name != "llama3:8b" || models[1].Name != "qwen3:8b" {
		t.Errorf("models are not sorted: %v", models)
	}
	if models[1].Digest != "abc" || models[1].ParameterSize != "8B" || models[1].SizeBytes != 123 {
		t.Errorf("model fields = %+v", models[1])
	}
}

func TestChatNonStream(t *testing.T) {
	var received map[string]any
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Errorf("body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hello","thinking":"hmm"},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":5,"total_duration":1000000}`)
	})

	think := true
	resp, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Think:    &think,
		Params: map[string]json.RawMessage{
			"temperature": json.RawMessage("0.2"),
			"top_k":       json.RawMessage("40"),
			"max_tokens":  json.RawMessage("96"),
			"keep_alive":  json.RawMessage(`"30m"`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.Thinking != "hmm" || resp.FinishReason != "stop" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 8 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Metrics.TotalDuration != time.Millisecond {
		t.Errorf("total duration = %v", resp.Metrics.TotalDuration)
	}
	if received["stream"] != false || received["think"] != true {
		t.Errorf("body = %v", received)
	}
	options, ok := received["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %v", received["options"])
	}
	if options["temperature"] != 0.2 || options["top_k"] != float64(40) || options["num_predict"] != float64(96) {
		t.Errorf("options = %v", options)
	}
	if received["keep_alive"] != "30m" {
		t.Errorf("keep_alive = %v", received["keep_alive"])
	}
}

func TestChatStream(t *testing.T) {
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		frames := []string{
			`{"model":"m","message":{"content":"Hel"},"done":false}`,
			`{"model":"m","message":{"thinking":"think"},"done":false}`,
			`{"model":"m","message":{"content":"lo"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":3}`,
		}
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n")
			flusher.Flush()
		}
	})

	var events []provider.StreamEvent
	resp, err := p.ChatStream(context.Background(), provider.ChatRequest{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, func(event provider.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello" || resp.Thinking != "think" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 2 || resp.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if len(events) != 5 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Type != provider.EventDelta || events[0].Text != "Hel" {
		t.Errorf("event 0 = %+v", events[0])
	}
	if events[1].Type != provider.EventThinking || events[1].Text != "think" {
		t.Errorf("event 1 = %+v", events[1])
	}
	if events[3].Type != provider.EventUsage || events[3].Usage == nil {
		t.Errorf("event 3 = %+v", events[3])
	}
	if events[4].Type != provider.EventDone || !events[4].Done {
		t.Errorf("event 4 = %+v", events[4])
	}
	if len(resp.Raw) == 0 || !json.Valid(resp.Raw) {
		t.Errorf("raw stream is not valid JSON: %s", resp.Raw)
	}
}

func TestHTTPError(t *testing.T) {
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"busy"}`)
	})
	_, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	httpErr, ok := provider.IsHTTPError(err)
	if !ok {
		t.Fatalf("err = %v, want HTTPError", err)
	}
	if httpErr.Status != http.StatusServiceUnavailable || !strings.Contains(string(httpErr.Body), "busy") {
		t.Errorf("http error = %+v", httpErr)
	}
}

func TestCancel(t *testing.T) {
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// Keep the response open long enough for the client deadline to win.
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"done":true}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := p.ChatStream(ctx, provider.ChatRequest{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
}

func TestMissingModel(t *testing.T) {
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called")
	})
	_, err := p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Errorf("err = %v", err)
	}
}

func TestNewRejectsBadBase(t *testing.T) {
	for _, raw := range []string{"", "ftp://x"} {
		base, _ := url.Parse(raw)
		if _, err := New(base, nil); err == nil {
			t.Errorf("New(%q) succeeded", raw)
		}
	}
	if _, err := New(nil, nil); err == nil {
		t.Error("New(nil) succeeded")
	}
}

func TestCapabilities(t *testing.T) {
	p := newProvider(t, func(w http.ResponseWriter, r *http.Request) {})
	caps := p.Capabilities()
	if !caps.Streaming || !caps.Usage || caps.Thinking != provider.ThinkingBool {
		t.Errorf("capabilities = %+v", caps)
	}
	found := false
	for _, name := range caps.SamplingParams {
		if name == "top_k" {
			found = true
		}
	}
	if !found {
		t.Errorf("top_k missing from capabilities: %v", caps.SamplingParams)
	}
}

func TestEndpoint(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:11434/base/")
	p, _ := New(base, nil)
	if got := p.endpoint("/api/chat"); got != "http://127.0.0.1:11434/base/api/chat" {
		t.Errorf("endpoint = %s", got)
	}
}

func BenchmarkBuildBody(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := buildBody(provider.ChatRequest{
			Model:    "m",
			Messages: []provider.Message{{Role: "user", Content: strings.Repeat("x", 1000)}},
			Params: map[string]json.RawMessage{
				"temperature": json.RawMessage("0.2"),
				"top_k":       json.RawMessage("40"),
			},
		}, true); err != nil {
			b.Fatal(err)
		}
	}
}

func ExampleProvider() {
	fmt.Println(DefaultID)
	// Output: ollama
}
