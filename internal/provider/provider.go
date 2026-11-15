// Package provider defines the small interface the workbench uses to talk to
// a model runtime. It deliberately describes only what one verified backend
// (Ollama) needs today; more providers are added when there is a real backend
// to test against, not before.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Message is one chat message in a provider-neutral shape.
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

// Model describes one model exposed by a provider.
type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Digest        string `json:"digest,omitempty"`
	Family        string `json:"family,omitempty"`
	ParameterSize string `json:"parameter_size,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
}

// ThinkingSupport describes how a provider controls reasoning.
type ThinkingSupport string

const (
	ThinkingNone   ThinkingSupport = "none"
	ThinkingBool   ThinkingSupport = "bool"
	ThinkingLevels ThinkingSupport = "levels"
)

// Capabilities lets the UI hide controls a backend cannot honor.
type Capabilities struct {
	Streaming      bool            `json:"streaming"`
	Thinking       ThinkingSupport `json:"thinking"`
	Usage          bool            `json:"usage"`
	SystemRole     bool            `json:"system_role"`
	ToolCalls      bool            `json:"tool_calls"`
	SamplingParams []string        `json:"sampling_params,omitempty"`
}

// Usage is normalized token accounting. Providers that do not report usage
// leave it nil.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Metrics carries backend timings when available.
type Metrics struct {
	TotalDuration      time.Duration `json:"-"`
	LoadDuration       time.Duration `json:"-"`
	PromptEvalDuration time.Duration `json:"-"`
	EvalDuration       time.Duration `json:"-"`
}

// ChatRequest is a fully resolved provider call. Policy has already decided
// the values; the provider only maps them onto its wire format.
type ChatRequest struct {
	Model    string                     `json:"model"`
	Messages []Message                  `json:"messages"`
	Params   map[string]json.RawMessage `json:"params,omitempty"`
	// Think is nil when the policy mode is passthrough.
	Think *bool `json:"think,omitempty"`
}

// ChatResponse is the aggregated result of a call, including streams.
type ChatResponse struct {
	Model        string
	ModelDigest  string
	Text         string
	Thinking     string
	FinishReason string
	Usage        *Usage
	Metrics      Metrics
	// Raw is the backend response body, or the bounded raw stream frames.
	Raw json.RawMessage
}

// EventType is a stream event kind.
type EventType string

const (
	EventDelta    EventType = "delta"
	EventThinking EventType = "thinking"
	EventUsage    EventType = "usage"
	EventDone     EventType = "done"
)

// StreamEvent is one incremental update.
type StreamEvent struct {
	Type  EventType
	Text  string
	Usage *Usage
	Done  bool
}

// Provider is the runtime adapter. Implementations must honor ctx
// cancellation and must not retry automatically.
type Provider interface {
	ID() string
	Name() string
	Capabilities() Capabilities
	ListModels(ctx context.Context) ([]Model, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) (ChatResponse, error)
}

// HTTPError preserves an upstream status and a bounded body.
type HTTPError struct {
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	body := string(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("upstream returned %d: %s", e.Status, body)
}

// StatusCode reports the upstream HTTP status.
func (e *HTTPError) StatusCode() int { return e.Status }

// IsHTTPError extracts an *HTTPError.
func IsHTTPError(err error) (*HTTPError, bool) {
	var target *HTTPError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// Client is a convenience HTTP client for providers. It has no total timeout
// so a long stream is not cut off; callers bound calls with a context.
func Client(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: timeout,
		// Streaming bodies must arrive incrementally.
		DisableCompression: true,
	}
	return &http.Client{Transport: transport}
}
