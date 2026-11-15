// Package engine orchestrates semantic model calls for the workbench. The
// transparent proxy does not use it: it only forwards raw requests. Keeping
// the two paths separate is what lets the proxy stay byte-faithful while the
// playground gets parsed events, usage and timings.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jachy-h/llm-workbench/internal/policy"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/requestid"
)

// Engine resolves providers and applies policy before a call.
type Engine struct {
	providers       map[string]provider.Provider
	defaultProvider string
	now             func() time.Time
}

// New builds an engine. defaultProvider is used when a request omits one.
func New(providers map[string]provider.Provider, defaultProvider string) (*Engine, error) {
	if len(providers) == 0 {
		return nil, errors.New("engine: no providers configured")
	}
	if defaultProvider == "" {
		for id := range providers {
			defaultProvider = id
			break
		}
	}
	if _, ok := providers[defaultProvider]; !ok {
		return nil, fmt.Errorf("engine: default provider %q is not registered", defaultProvider)
	}
	return &Engine{providers: providers, defaultProvider: defaultProvider, now: time.Now}, nil
}

// Providers returns the registered providers.
func (e *Engine) Providers() map[string]provider.Provider {
	out := make(map[string]provider.Provider, len(e.providers))
	for id, p := range e.providers {
		out[id] = p
	}
	return out
}

// DefaultProvider is used when a request omits a provider ID.
func (e *Engine) DefaultProvider() string { return e.defaultProvider }

// Request is a semantic call request as received from the playground or an
// experiment. Params are already canonical names (temperature, top_k, ...).
type Request struct {
	Provider     string
	Model        string
	Messages     []provider.Message
	SystemPrompt policy.SystemPrompt
	Thinking     policy.ThinkingMode
	Params       map[string]json.RawMessage
	Stream       bool
	// RequestID lets the caller generate the ID first (so a streaming API can
	// emit a meta event before the provider responds). Empty means generate.
	RequestID string
}

// Result carries timing and the aggregated response.
type Result struct {
	RequestID string
	Provider  string
	Response  provider.ChatResponse
	Prepared  provider.ChatRequest
	Duration  time.Duration
	TTFT      time.Duration
	Stream    bool
}

// Chat performs a non-streaming call.
func (e *Engine) Chat(ctx context.Context, req Request) (Result, error) {
	started := e.now()
	id := resolveID(req)
	prepared, p, err := e.prepare(req)
	if err != nil {
		return Result{RequestID: id}, err
	}
	response, err := p.Chat(ctx, prepared)
	duration := e.now().Sub(started)
	return Result{
		RequestID: id,
		Provider:  p.ID(),
		Response:  response,
		Prepared:  prepared,
		Duration:  duration,
		TTFT:      duration,
		Stream:    false,
	}, err
}

// ChatStream performs a streaming call and forwards provider events. A nil
// emit still drains the stream, which is useful for experiments.
func (e *Engine) ChatStream(ctx context.Context, req Request, emit func(provider.StreamEvent) error) (Result, error) {
	started := e.now()
	id := resolveID(req)
	prepared, p, err := e.prepare(req)
	if err != nil {
		return Result{RequestID: id}, err
	}
	if !p.Capabilities().Streaming {
		return Result{RequestID: id}, fmt.Errorf("engine: provider %q does not support streaming", p.ID())
	}
	var firstToken time.Duration
	wrapped := func(event provider.StreamEvent) error {
		if firstToken == 0 && (event.Type == provider.EventDelta || event.Type == provider.EventThinking) {
			firstToken = e.now().Sub(started)
		}
		if emit != nil {
			return emit(event)
		}
		return nil
	}
	response, err := p.ChatStream(ctx, prepared, wrapped)
	duration := e.now().Sub(started)
	if firstToken == 0 {
		firstToken = duration
	}
	return Result{
		RequestID: id,
		Provider:  p.ID(),
		Response:  response,
		Prepared:  prepared,
		Duration:  duration,
		TTFT:      firstToken,
		Stream:    true,
	}, err
}

func resolveID(req Request) string {
	if req.RequestID != "" {
		return req.RequestID
	}
	return requestid.New()
}

func (e *Engine) prepare(req Request) (provider.ChatRequest, provider.Provider, error) {
	if len(req.Messages) == 0 {
		return provider.ChatRequest{}, nil, errors.New("engine: messages must not be empty")
	}
	if req.Model == "" {
		return provider.ChatRequest{}, nil, errors.New("engine: model must not be empty")
	}
	id := req.Provider
	if id == "" {
		id = e.defaultProvider
	}
	p, ok := e.providers[id]
	if !ok {
		return provider.ChatRequest{}, nil, fmt.Errorf("engine: unknown provider %q", id)
	}
	thinking := req.Thinking
	if thinking == "" {
		thinking = policy.ThinkingOff
	}
	return provider.ChatRequest{
		Model:    req.Model,
		Messages: applySystem(req.Messages, req.SystemPrompt),
		Params:   cloneParams(req.Params),
		Think:    policy.ThinkValue(thinking),
	}, p, nil
}

func applySystem(messages []provider.Message, sp policy.SystemPrompt) []provider.Message {
	typed := make([]policy.Message, 0, len(messages))
	for _, message := range messages {
		typed = append(typed, policy.Message{Role: message.Role, Content: message.Content, Images: message.Images})
	}
	typed = policy.ApplySystemMessages(typed, sp)
	out := make([]provider.Message, 0, len(typed))
	for _, message := range typed {
		out = append(out, provider.Message{Role: message.Role, Content: message.Content, Images: message.Images})
	}
	return out
}

func cloneParams(params map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(params))
	for key, value := range params {
		out[key] = value
	}
	return out
}
