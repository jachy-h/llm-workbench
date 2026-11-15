package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func params(raw string) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(err)
	}
	return out
}

func decode(t *testing.T, m map[string]json.RawMessage) map[string]any {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyV1DefaultMatchesHistoricalBehavior(t *testing.T) {
	out := ApplyV1(params(`{"model":"m","temperature":1}`), New(params(`{"temperature":0.2,"top_p":0.9,"max_tokens":96,"keep_alive":"30m"}`)))
	got := decode(t, out)
	if got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v", got["reasoning_effort"])
	}
	if got["temperature"] != float64(1) {
		t.Errorf("client temperature was overwritten: %v", got["temperature"])
	}
	if got["max_tokens"] != float64(96) || got["top_p"] != 0.9 {
		t.Errorf("missing params were not injected: %v", got)
	}
	if _, present := got["think"]; present {
		t.Error("think must be removed on /v1")
	}
}

func TestThinkingModesV1(t *testing.T) {
	cases := []struct {
		mode      ThinkingMode
		effort    string
		hasEffort bool
		hasThink  bool
	}{
		{ThinkingOff, "none", true, false},
		{ThinkingOn, "high", true, false},
		{ThinkingPassthrough, "high", true, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			out := ApplyV1(params(`{"model":"m","reasoning_effort":"high","think":true}`), Policy{Thinking: tc.mode})
			got := decode(t, out)
			effort, has := got["reasoning_effort"]
			if has != tc.hasEffort || (has && effort != tc.effort) {
				t.Errorf("reasoning_effort = %v (present %v)", effort, has)
			}
			_, hasThink := got["think"]
			if hasThink != tc.hasThink {
				t.Errorf("think present = %v, want %v", hasThink, tc.hasThink)
			}
		})
	}
}

func TestThinkingModesNative(t *testing.T) {
	cases := []struct {
		mode  ThinkingMode
		think bool
	}{
		{ThinkingOff, false},
		{ThinkingOn, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			out, ok := ApplyNative(params(`{"model":"m","think":false,"reasoning_effort":"high"}`), Policy{Thinking: tc.mode})
			if !ok {
				t.Fatal("ApplyNative reported !ok")
			}
			got := decode(t, out)
			if got["think"] != tc.think {
				t.Errorf("think = %v, want %v", got["think"], tc.think)
			}
			if _, present := got["reasoning_effort"]; present {
				t.Error("reasoning_effort must be removed on /api/chat")
			}
		})
	}
	// Passthrough must keep both fields exactly as sent.
	out, ok := ApplyNative(params(`{"model":"m","think":true,"reasoning_effort":"high"}`), Policy{Thinking: ThinkingPassthrough})
	if !ok {
		t.Fatal("ApplyNative reported !ok")
	}
	got := decode(t, out)
	if got["think"] != true || got["reasoning_effort"] != "high" {
		t.Errorf("passthrough changed fields: %v", got)
	}
}

func TestNativeTopKGoesIntoOptions(t *testing.T) {
	out, ok := ApplyNative(params(`{"model":"m","options":{"num_predict":7}}`),
		Policy{Thinking: ThinkingOff, Params: params(`{"top_k":40,"max_tokens":96,"temperature":0.2,"keep_alive":"30m"}`)})
	if !ok {
		t.Fatal("ApplyNative reported !ok")
	}
	got := decode(t, out)
	options := got["options"].(map[string]any)
	if options["top_k"] != float64(40) {
		t.Errorf("options.top_k = %v, want 40", options["top_k"])
	}
	if options["num_predict"] != float64(7) {
		t.Errorf("client num_predict was overwritten: %v", options["num_predict"])
	}
	if got["keep_alive"] != "30m" {
		t.Errorf("keep_alive = %v, want top-level", got["keep_alive"])
	}
	if _, present := got["top_k"]; present {
		t.Error("top_k leaked to the top level")
	}
}

func TestNativePrefersNumPredictOverMaxTokens(t *testing.T) {
	out, ok := ApplyNative(params(`{"model":"m"}`),
		Policy{Thinking: ThinkingOff, Params: params(`{"max_tokens":96,"num_predict":12}`)})
	if !ok {
		t.Fatal("ApplyNative reported !ok")
	}
	options := decode(t, out)["options"].(map[string]any)
	if options["num_predict"] != float64(12) {
		t.Errorf("num_predict = %v, want 12", options["num_predict"])
	}
}

func TestSplitNativeParams(t *testing.T) {
	options, topLevel := SplitNativeParams(params(`{"temperature":0.2,"top_k":40,"max_tokens":96,"keep_alive":"30m"}`))
	if string(options["temperature"]) != "0.2" || string(options["top_k"]) != "40" || string(options["num_predict"]) != "96" {
		t.Errorf("options = %v", options)
	}
	if string(topLevel["keep_alive"]) != `"30m"` {
		t.Errorf("top level = %v", topLevel)
	}
	if _, present := topLevel["temperature"]; present {
		t.Error("temperature leaked to the top level")
	}
}

func TestSystemPromptRaw(t *testing.T) {
	body := func(raw string) map[string]json.RawMessage { return params(raw) }

	t.Run("off", func(t *testing.T) {
		out, ok := ApplySystemPrompt(body(`{"messages":[{"role":"user","content":"hi"}]}`),
			SystemPrompt{Mode: SystemPromptOff})
		if !ok {
			t.Fatal("unexpected failure")
		}
		if string(out["messages"]) != `[{"role":"user","content":"hi"}]` {
			t.Errorf("messages changed: %s", out["messages"])
		}
	})

	t.Run("inject when missing", func(t *testing.T) {
		out, ok := ApplySystemPrompt(body(`{"messages":[{"role":"user","content":"hi"}]}`),
			SystemPrompt{Mode: SystemPromptInjectIfMissing, Text: "be terse"})
		if !ok {
			t.Fatal("unexpected failure")
		}
		got := decode(t, out)["messages"].([]any)
		if len(got) != 2 {
			t.Fatalf("messages = %v", got)
		}
		first := got[0].(map[string]any)
		if first["role"] != "system" || first["content"] != "be terse" {
			t.Errorf("first message = %v", first)
		}
	})

	t.Run("existing system wins", func(t *testing.T) {
		out, ok := ApplySystemPrompt(body(`{"messages":[{"role":"system","content":"keep me"},{"role":"user","content":"hi"}]}`),
			SystemPrompt{Mode: SystemPromptInjectIfMissing, Text: "drop me"})
		if !ok {
			t.Fatal("unexpected failure")
		}
		got := decode(t, out)["messages"].([]any)
		if len(got) != 2 || got[0].(map[string]any)["content"] != "keep me" {
			t.Errorf("messages = %v", got)
		}
	})

	t.Run("override removes system and developer", func(t *testing.T) {
		out, ok := ApplySystemPrompt(body(`{"messages":[{"role":"system","content":"old"},{"role":"developer","content":"old2"},{"role":"user","content":"hi"}]}`),
			SystemPrompt{Mode: SystemPromptOverride, Text: "new"})
		if !ok {
			t.Fatal("unexpected failure")
		}
		got := decode(t, out)["messages"].([]any)
		if len(got) != 2 {
			t.Fatalf("messages = %v", got)
		}
		if got[0].(map[string]any)["content"] != "new" || got[1].(map[string]any)["content"] != "hi" {
			t.Errorf("messages = %v", got)
		}
	})

	t.Run("invalid messages pass through", func(t *testing.T) {
		out, ok := ApplySystemPrompt(body(`{"messages":"not an array"}`),
			SystemPrompt{Mode: SystemPromptInjectIfMissing, Text: "x"})
		if ok {
			t.Fatal("expected ok=false")
		}
		if string(out["messages"]) != `"not an array"` {
			t.Errorf("messages changed: %s", out["messages"])
		}
	})

	t.Run("unknown message fields survive", func(t *testing.T) {
		out, ok := ApplySystemPrompt(body(`{"messages":[{"role":"user","content":"hi","images":["a.png"]}]}`),
			SystemPrompt{Mode: SystemPromptInjectIfMissing, Text: "x"})
		if !ok {
			t.Fatal("unexpected failure")
		}
		if !strings.Contains(string(out["messages"]), `"images":["a.png"]`) {
			t.Errorf("unknown field was dropped: %s", out["messages"])
		}
	})
}

func TestTypedSystemPromptMatchesRawRule(t *testing.T) {
	messages := []Message{{Role: "user", Content: "hi"}}
	out := ApplySystemMessages(messages, SystemPrompt{Mode: SystemPromptInjectIfMissing, Text: "be terse"})
	if len(out) != 2 || out[0].Role != "system" || out[0].Content != "be terse" {
		t.Errorf("messages = %+v", out)
	}
	out = ApplySystemMessages([]Message{{Role: "system", Content: "keep"}, {Role: "user", Content: "hi"}},
		SystemPrompt{Mode: SystemPromptOverride, Text: "new"})
	if len(out) != 2 || out[0].Content != "new" {
		t.Errorf("messages = %+v", out)
	}
}

func TestThinkingParsing(t *testing.T) {
	for _, valid := range []string{"off", "on", "passthrough", " OFF "} {
		if _, ok := ParseThinkingMode(valid); !ok {
			t.Errorf("ParseThinkingMode(%q) failed", valid)
		}
	}
	for _, invalid := range []string{"", "yes", "none"} {
		if _, ok := ParseThinkingMode(invalid); ok {
			t.Errorf("ParseThinkingMode(%q) succeeded", invalid)
		}
	}
}
