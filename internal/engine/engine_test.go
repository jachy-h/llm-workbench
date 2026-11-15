package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/policy"
	"github.com/jachy-h/llm-workbench/internal/provider"
)

type fakeProvider struct {
	last      provider.ChatRequest
	response  provider.ChatResponse
	stream    []provider.StreamEvent
	chatErr   error
	streamErr error
}

func (f *fakeProvider) ID() string   { return "fake" }
func (f *fakeProvider) Name() string { return "Fake" }
func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Streaming: true, Thinking: provider.ThinkingBool, Usage: true}
}
func (f *fakeProvider) ListModels(context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "m", Name: "m"}}, nil
}
func (f *fakeProvider) Chat(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	f.last = req
	return f.response, f.chatErr
}
func (f *fakeProvider) ChatStream(_ context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) (provider.ChatResponse, error) {
	f.last = req
	for _, event := range f.stream {
		if emit != nil {
			if err := emit(event); err != nil {
				return provider.ChatResponse{}, err
			}
		}
	}
	return f.response, f.streamErr
}

func newEngine(t *testing.T, f *fakeProvider) *Engine {
	t.Helper()
	e, err := New(map[string]provider.Provider{"fake": f}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return time.Unix(0, 0) }
	return e
}

func TestChatAppliesSystemPromptAndThinking(t *testing.T) {
	f := &fakeProvider{response: provider.ChatResponse{Text: "ok"}}
	e := newEngine(t, f)
	result, err := e.Chat(context.Background(), Request{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		SystemPrompt: policy.SystemPrompt{
			Mode: policy.SystemPromptInjectIfMissing,
			Text: "be terse",
		},
		Thinking: policy.ThinkingOff,
		Params:   map[string]json.RawMessage{"temperature": json.RawMessage("0.2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID == "" || !strings.HasPrefix(result.RequestID, "req_") {
		t.Errorf("request id = %q", result.RequestID)
	}
	if f.last.Think == nil || *f.last.Think != false {
		t.Errorf("think = %v, want false", f.last.Think)
	}
	if len(f.last.Messages) != 2 || f.last.Messages[0].Role != "system" || f.last.Messages[0].Content != "be terse" {
		t.Errorf("messages = %+v", f.last.Messages)
	}
	if string(f.last.Params["temperature"]) != "0.2" {
		t.Errorf("params = %v", f.last.Params)
	}
	if result.Duration != 0 || result.TTFT != 0 {
		t.Errorf("timings with fixed clock = %v/%v", result.Duration, result.TTFT)
	}
}

func TestChatThinkingPassthroughOmitsFlag(t *testing.T) {
	f := &fakeProvider{}
	e := newEngine(t, f)
	if _, err := e.Chat(context.Background(), Request{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Thinking: policy.ThinkingPassthrough,
	}); err != nil {
		t.Fatal(err)
	}
	if f.last.Think != nil {
		t.Errorf("think = %v, want nil", *f.last.Think)
	}
}

func TestChatStreamForwardsEvents(t *testing.T) {
	f := &fakeProvider{
		stream: []provider.StreamEvent{
			{Type: provider.EventDelta, Text: "a"},
			{Type: provider.EventDelta, Text: "b"},
			{Type: provider.EventDone, Done: true},
		},
		response: provider.ChatResponse{Text: "ab"},
	}
	e := newEngine(t, f)
	var got []string
	result, err := e.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, func(event provider.StreamEvent) error {
		got = append(got, string(event.Type)+":"+event.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "delta:a" || got[1] != "delta:b" {
		t.Errorf("events = %v", got)
	}
	if result.Response.Text != "ab" || !result.Stream {
		t.Errorf("result = %+v", result)
	}
}

func TestChatStreamEmitErrorStops(t *testing.T) {
	f := &fakeProvider{stream: []provider.StreamEvent{{Type: provider.EventDelta, Text: "a"}, {Type: provider.EventDelta, Text: "b"}}}
	e := newEngine(t, f)
	_, err := e.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, func(provider.StreamEvent) error { return errors.New("client gone") })
	if err == nil || !strings.Contains(err.Error(), "client gone") {
		t.Errorf("err = %v", err)
	}
}

func TestUnknownProviderAndBadRequest(t *testing.T) {
	e := newEngine(t, &fakeProvider{})
	if _, err := e.Chat(context.Background(), Request{Provider: "nope", Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Error("unknown provider was accepted")
	}
	if _, err := e.Chat(context.Background(), Request{Model: "m"}); err == nil {
		t.Error("empty messages were accepted")
	}
	if _, err := e.Chat(context.Background(), Request{Messages: []provider.Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Error("empty model was accepted")
	}
}

func TestNewValidatesProviders(t *testing.T) {
	if _, err := New(nil, ""); err == nil {
		t.Error("empty provider map was accepted")
	}
	if _, err := New(map[string]provider.Provider{"a": &fakeProvider{}}, "b"); err == nil {
		t.Error("missing default provider was accepted")
	}
	e, err := New(map[string]provider.Provider{"a": &fakeProvider{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Providers()) != 1 {
		t.Errorf("providers = %v", e.Providers())
	}
}

func TestChatUsesCallerRequestID(t *testing.T) {
	f := &fakeProvider{}
	e := newEngine(t, f)
	result, err := e.Chat(context.Background(), Request{
		Model:     "m",
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
		RequestID: "req_caller",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "req_caller" {
		t.Errorf("request id = %q", result.RequestID)
	}
}

func TestSystemPromptOverrideDropsOldSystem(t *testing.T) {
	f := &fakeProvider{}
	e := newEngine(t, f)
	if _, err := e.Chat(context.Background(), Request{
		Model: "m",
		Messages: []provider.Message{
			{Role: "system", Content: "old"},
			{Role: "user", Content: "hi"},
		},
		SystemPrompt: policy.SystemPrompt{Mode: policy.SystemPromptOverride, Text: "new"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.last.Messages) != 2 || f.last.Messages[0].Content != "new" || f.last.Messages[1].Content != "hi" {
		t.Errorf("messages = %+v", f.last.Messages)
	}
}
