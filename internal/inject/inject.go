// Package inject adapts the policy rules to the proxy's raw JSON bodies.
//
// The actual rewriting lives in internal/policy; this package only keeps the
// historical API (InjectV1 / InjectNative) and the path matching used by the
// transparent proxy. Keeping it thin prevents a second parameter table from
// growing here.
package inject

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/jachy-h/llm-workbench/internal/policy"
)

// Kind is the chat-flavour of a request path.
type Kind int

const (
	// KindNone means "not a chat path": pass through untouched.
	KindNone Kind = iota
	// KindV1 is /v1/chat/completions (OpenAI compatible).
	KindV1
	// KindNative is /api/chat (Ollama native).
	KindNative
)

// Paths are exact matches on the URL path (query string already stripped).
var (
	v1ChatPaths     = []string{"/v1/chat/completions"}
	nativeChatPaths = []string{"/api/chat"}
)

// ChatPathKind reports which injection applies to path.
func ChatPathKind(path string) Kind {
	switch {
	case slices.Contains(v1ChatPaths, path):
		return KindV1
	case slices.Contains(nativeChatPaths, path):
		return KindNative
	default:
		return KindNone
	}
}

// InjectV1 applies the historical default policy: thinking off and only the
// missing params filled in. Kept for callers and tests that do not need a
// configurable policy.
func InjectV1(
	body map[string]json.RawMessage,
	params map[string]json.RawMessage,
) map[string]json.RawMessage {
	return policy.ApplyV1(body, policy.New(params))
}

// InjectNative is the historical native default: think:false and missing
// params filled into options. ok is false when "options" is not an object.
func InjectNative(
	body map[string]json.RawMessage,
	params map[string]json.RawMessage,
) (out map[string]json.RawMessage, ok bool) {
	return policy.ApplyNative(body, policy.New(params))
}

// ApplyV1 rewrites an OpenAI-compatible request with an explicit policy.
func ApplyV1(
	body map[string]json.RawMessage,
	p policy.Policy,
) map[string]json.RawMessage {
	return policy.ApplyV1(body, p)
}

// ApplyNative rewrites an Ollama native request with an explicit policy.
func ApplyNative(
	body map[string]json.RawMessage,
	p policy.Policy,
) (out map[string]json.RawMessage, ok bool) {
	return policy.ApplyNative(body, p)
}

// Encode serialises a body without HTML escaping and without a trailing
// newline, so "<" in a prompt stays "<" on the wire.
func Encode(body map[string]json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// DecodeObject parses raw as a JSON object. Anything else (array, string,
// number, null, syntax error) reports ok=false so the proxy passes such
// bodies through untouched.
func DecodeObject(raw []byte) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, false
	}
	return body, true
}
