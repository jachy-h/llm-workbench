package inject

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

func TestV1ForcesReasoningEffortNone(t *testing.T) {
	out := InjectV1(params(`{"model":"m","messages":[]}`),
		params(`{"temperature":0.2,"top_p":0.9,"max_tokens":96,"keep_alive":"30m"}`))
	got := decode(t, out)
	if got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want none", got["reasoning_effort"])
	}
	if got["max_tokens"] != float64(96) {
		t.Errorf("max_tokens = %v, want 96", got["max_tokens"])
	}
	if got["keep_alive"] != "30m" {
		t.Errorf("keep_alive = %v", got["keep_alive"])
	}
}

func TestV1OverridesClientReasoningEffort(t *testing.T) {
	for _, value := range []string{`"high"`, `"low"`, `null`, `"none"`} {
		out := InjectV1(params(`{"model":"m","reasoning_effort":`+value+`}`), nil)
		if string(out["reasoning_effort"]) != `"none"` {
			t.Errorf("client reasoning_effort=%s was not overwritten: %s", value, out["reasoning_effort"])
		}
	}
}

func TestV1DropsThink(t *testing.T) {
	out := InjectV1(params(`{"model":"m","think":false}`), nil)
	if _, ok := out["think"]; ok {
		t.Error("think should be removed on /v1")
	}
}

func TestClientValuesWin(t *testing.T) {
	out := InjectV1(params(`{"model":"m","temperature":0,"max_tokens":8}`),
		params(`{"temperature":0.2,"max_tokens":96}`))
	if string(out["temperature"]) != "0" {
		t.Errorf("temperature = %s, want client value 0", out["temperature"])
	}
	if string(out["max_tokens"]) != "8" {
		t.Errorf("max_tokens = %s, want client value 8", out["max_tokens"])
	}
}

func TestV1PreservesUnknownFieldsVerbatim(t *testing.T) {
	body := params(`{"model":"m","big":12345678901234567890,"nested":{"a":[1,2,{"b":null}]},"pi":3.14159265358979323846}`)
	out := InjectV1(body, params(`{"temperature":0.2}`))
	if string(out["big"]) != "12345678901234567890" {
		t.Errorf("big int not preserved: %s", out["big"])
	}
	if string(out["pi"]) != "3.14159265358979323846" {
		t.Errorf("float not preserved: %s", out["pi"])
	}
	if string(out["nested"]) != `{"a":[1,2,{"b":null}]}` {
		t.Errorf("nested object changed: %s", out["nested"])
	}
}

func TestNativeMapsMaxTokensToNumPredict(t *testing.T) {
	out, ok := InjectNative(params(`{"model":"m"}`),
		params(`{"temperature":0.2,"top_p":0.9,"max_tokens":96,"keep_alive":"30m"}`))
	if !ok {
		t.Fatal("InjectNative reported !ok")
	}
	got := decode(t, out)
	if got["think"] != false {
		t.Errorf("think = %v, want false", got["think"])
	}
	options, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %v", got["options"])
	}
	if options["num_predict"] != float64(96) {
		t.Errorf("options.num_predict = %v", options["num_predict"])
	}
	if options["temperature"] != 0.2 || options["top_p"] != 0.9 {
		t.Errorf("options = %v", options)
	}
	if got["keep_alive"] != "30m" {
		t.Errorf("keep_alive = %v, want top-level", got["keep_alive"])
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Error("reasoning_effort should be removed on /api/chat")
	}
}

func TestNativeForcesThinkFalse(t *testing.T) {
	out, ok := InjectNative(params(`{"model":"m","think":true}`), nil)
	if !ok {
		t.Fatal("InjectNative reported !ok")
	}
	if string(out["think"]) != "false" {
		t.Errorf("think = %s, want false", out["think"])
	}
}

func TestNativeKeepsClientOptions(t *testing.T) {
	out, ok := InjectNative(params(`{"model":"m","options":{"num_predict":7,"num_ctx":8192}}`),
		params(`{"max_tokens":96,"temperature":0.2}`))
	if !ok {
		t.Fatal("InjectNative reported !ok")
	}
	got := decode(t, out)
	options := got["options"].(map[string]any)
	if options["num_predict"] != float64(7) {
		t.Errorf("options.num_predict = %v, want 7", options["num_predict"])
	}
	if options["num_ctx"] != float64(8192) {
		t.Errorf("options.num_ctx = %v, want 8192", options["num_ctx"])
	}
	if options["temperature"] != 0.2 {
		t.Errorf("options.temperature = %v, want injected 0.2", options["temperature"])
	}
}

func TestNativeNoParamsKeepsOptions(t *testing.T) {
	out, ok := InjectNative(params(`{"model":"m","options":{"num_predict":7}}`), nil)
	if !ok {
		t.Fatal("InjectNative reported !ok")
	}
	got := decode(t, out)
	if got["options"].(map[string]any)["num_predict"] != float64(7) {
		t.Errorf("options were dropped: %v", got["options"])
	}
	if got["think"] != false {
		t.Errorf("think = %v", got["think"])
	}
}

func TestNativeNullOptions(t *testing.T) {
	out, ok := InjectNative(params(`{"model":"m","options":null}`), params(`{"max_tokens":96}`))
	if !ok {
		t.Fatal("InjectNative reported !ok")
	}
	got := decode(t, out)
	if got["options"].(map[string]any)["num_predict"] != float64(96) {
		t.Errorf("options = %v", got["options"])
	}
}

func TestNativeInvalidOptionsPassesThrough(t *testing.T) {
	body := params(`{"model":"m","options":[1,2]}`)
	out, ok := InjectNative(body, params(`{"max_tokens":96}`))
	if ok {
		t.Fatal("expected ok=false for non-object options")
	}
	if string(out["options"]) != "[1,2]" {
		t.Errorf("options changed: %s", out["options"])
	}
	if string(out["think"]) != "" {
		t.Error("pass-through must not inject think")
	}
}

func TestV1DoesNotMutateInput(t *testing.T) {
	body := params(`{"model":"m"}`)
	InjectV1(body, params(`{"temperature":0.2}`))
	if _, ok := body["temperature"]; ok {
		t.Error("InjectV1 mutated its input")
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("InjectV1 mutated its input")
	}
}

func TestChatPathKind(t *testing.T) {
	cases := map[string]Kind{
		"/v1/chat/completions":  KindV1,
		"/api/chat":             KindNative,
		"/v1/models":            KindNone,
		"/api/tags":             KindNone,
		"/api/chat/":            KindNone,
		"/v1/chat/completions/": KindNone,
		"":                      KindNone,
	}
	for path, want := range cases {
		if got := ChatPathKind(path); got != want {
			t.Errorf("ChatPathKind(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestDecodeObject(t *testing.T) {
	if _, ok := DecodeObject([]byte(`{"a":1}`)); !ok {
		t.Error("object should decode")
	}
	for _, raw := range []string{
		`[]`, `"str"`, `5`, `null`, ``, `   `, `{"a":}`, `{"a":1} trailing`,
	} {
		if _, ok := DecodeObject([]byte(raw)); ok {
			t.Errorf("DecodeObject(%q) succeeded, want pass-through", raw)
		}
	}
}

func TestEncodeKeepsMarkup(t *testing.T) {
	body := params(`{"messages":[{"content":"a < b & c > d"}]}`)
	raw, err := Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u003c`) || strings.Contains(string(raw), `\u0026`) {
		t.Errorf("Encode HTML-escaped the body: %s", raw)
	}
	if !json.Valid(raw) {
		t.Errorf("Encode produced invalid JSON: %s", raw)
	}
	if raw[len(raw)-1] == '\n' {
		t.Error("Encode left a trailing newline")
	}
}
