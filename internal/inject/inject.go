// Package inject rewrites the two Ollama chat request bodies.
//
// The rewrite is deliberately narrow: only the thinking switch and the
// missing generation parameters are touched. Everything else is kept as
// json.RawMessage so unknown / future fields survive untouched and numbers
// keep their original textual form.
package inject

import (
	"bytes"
	"encoding/json"
	"slices"
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

// InjectV1 fills in missing params and forces reasoning_effort:"none".
//
// think is removed: on this path Ollama silently ignores it, keeping it
// would only mislead whoever inspects the request.
func InjectV1(
	body map[string]json.RawMessage,
	params map[string]json.RawMessage,
) map[string]json.RawMessage {
	out := clone(body)
	for key, value := range params {
		if _, ok := out[key]; !ok {
			out[key] = value
		}
	}
	out["reasoning_effort"] = json.RawMessage(`"none"`)
	delete(out, "think")
	return out
}

// nativeOptionKeys are the params that belong inside /api/chat "options".
var nativeOptionKeys = map[string]bool{
	"temperature":    true,
	"top_p":          true,
	"num_ctx":        true,
	"repeat_penalty": true,
	"seed":           true,
}

// InjectNative fills in missing params inside options and forces think:false.
//
// ok is false when the client sent an "options" member that is not a JSON
// object; in that case the caller must pass the request through unchanged
// instead of dropping a field it does not understand.
func InjectNative(
	body map[string]json.RawMessage,
	params map[string]json.RawMessage,
) (out map[string]json.RawMessage, ok bool) {
	out = clone(body)

	options := map[string]json.RawMessage{}
	hadOptions := false
	if raw, present := out["options"]; present {
		hadOptions = true
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			if err := json.Unmarshal(raw, &options); err != nil {
				return body, false
			}
		}
	}

	optionsChanged := false
	for key, value := range params {
		switch {
		case key == "max_tokens":
			if _, ok := options["num_predict"]; !ok {
				options["num_predict"] = value
				optionsChanged = true
			}
		case nativeOptionKeys[key]:
			if _, ok := options[key]; !ok {
				options[key] = value
				optionsChanged = true
			}
		default: // keep_alive and friends stay on the top level
			if _, ok := out[key]; !ok {
				out[key] = value
			}
		}
	}

	if (hadOptions || optionsChanged) && len(options) > 0 {
		raw, err := json.Marshal(options)
		if err != nil {
			return body, false
		}
		out["options"] = raw
	}
	out["think"] = json.RawMessage("false")
	delete(out, "reasoning_effort")
	return out, true
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

func clone(body map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(body)+4)
	for k, v := range body {
		out[k] = v
	}
	return out
}
