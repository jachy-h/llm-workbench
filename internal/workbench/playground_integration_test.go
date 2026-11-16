package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jachy-h/llm-workbench/internal/engine"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/provider/ollama"
	"github.com/jachy-h/llm-workbench/internal/storage"
)

func TestPlaygroundAgainstOllamaShapedUpstream(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"}]}`)
		case "/api/chat":
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &received); err != nil {
				t.Errorf("upstream body: %v", err)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			flusher := w.(http.Flusher)
			for _, frame := range []string{
				`{"model":"qwen3:8b","message":{"content":"he"},"done":false}`,
				`{"model":"qwen3:8b","message":{"content":"llo"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":3}`,
			} {
				_, _ = io.WriteString(w, frame+"\n")
				flusher.Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	base, _ := url.Parse(upstream.URL)
	ollamaProvider, err := ollama.New(base, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(map[string]provider.Provider{"ollama": ollamaProvider}, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	handler := New(Options{Version: "test", Store: store, Engine: eng})

	rec := call(handler, http.MethodGet, "/_/workbench/api/providers/ollama/models", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "qwen3:8b") {
		t.Fatalf("models = %d %s", rec.Code, rec.Body.String())
	}

	rec = call(handler, http.MethodPost, "/_/workbench/api/chat",
		`{"provider":"ollama","model":"qwen3:8b","messages":[{"role":"user","content":"hi"}],"params":{"temperature":0.2,"top_k":40},"think":false,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat = %d %s", rec.Code, rec.Body.String())
	}
	output := rec.Body.String()
	for _, needle := range []string{"event: meta", `"text":"he"`, `"text":"llo"`, "event: usage", "event: done", `"total_tokens":5`} {
		if !strings.Contains(output, needle) {
			t.Errorf("SSE missing %q:\n%s", needle, output)
		}
	}
	if received["think"] != false {
		t.Errorf("upstream think = %v", received["think"])
	}
	options, _ := received["options"].(map[string]any)
	if options["temperature"] != 0.2 || options["top_k"] != float64(40) {
		t.Errorf("upstream options = %v", received["options"])
	}

}
