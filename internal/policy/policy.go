// Package policy is the single source of truth for request rewriting.
//
// The transparent proxy works on raw JSON bodies and the semantic engine
// works on typed messages, but both must follow the same rules: which
// generation parameters exist, where they go for each API flavour, how
// thinking is switched, and when a system prompt is injected. Keeping those
// rules here prevents the proxy and the playground from drifting apart.
package policy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ThinkingMode controls how the thinking/reasoning switch is handled.
type ThinkingMode string

const (
	// ThinkingOff forces thinking off. This is the historical default.
	ThinkingOff ThinkingMode = "off"
	// ThinkingOn stops forcing thinking off; the client/provider default wins.
	ThinkingOn ThinkingMode = "on"
	// ThinkingPassthrough leaves every thinking-related field untouched.
	ThinkingPassthrough ThinkingMode = "passthrough"
)

// ParseThinkingMode reads a mode name. The empty string is not accepted.
func ParseThinkingMode(raw string) (ThinkingMode, bool) {
	switch ThinkingMode(strings.ToLower(strings.TrimSpace(raw))) {
	case ThinkingOff:
		return ThinkingOff, true
	case ThinkingOn:
		return ThinkingOn, true
	case ThinkingPassthrough:
		return ThinkingPassthrough, true
	default:
		return "", false
	}
}

// ThinkValue maps a mode to the native Ollama "think" flag. Nil means
// "omit the field and let the server decide".
func ThinkValue(mode ThinkingMode) *bool {
	switch mode {
	case ThinkingOff:
		value := false
		return &value
	case ThinkingOn:
		value := true
		return &value
	default:
		return nil
	}
}

// SystemPromptMode controls system-message injection.
type SystemPromptMode string

const (
	// SystemPromptOff leaves the messages untouched.
	SystemPromptOff SystemPromptMode = "off"
	// SystemPromptInjectIfMissing prepends a system message only when none
	// exists. Existing system/developer messages win.
	SystemPromptInjectIfMissing SystemPromptMode = "inject-if-missing"
	// SystemPromptOverride replaces every system/developer message with the
	// configured one. This is deliberately destructive and opt-in.
	SystemPromptOverride SystemPromptMode = "override"
)

// ParseSystemPromptMode reads a mode name.
func ParseSystemPromptMode(raw string) (SystemPromptMode, bool) {
	switch SystemPromptMode(strings.ToLower(strings.TrimSpace(raw))) {
	case SystemPromptOff:
		return SystemPromptOff, true
	case SystemPromptInjectIfMissing:
		return SystemPromptInjectIfMissing, true
	case SystemPromptOverride:
		return SystemPromptOverride, true
	default:
		return "", false
	}
}

// SystemPrompt is the configured system message and its injection mode.
type SystemPrompt struct {
	Mode SystemPromptMode `json:"mode"`
	Text string           `json:"text"`
}

// Message is the typed message shape used by the semantic engine. The
// transparent proxy keeps raw JSON instead, because unknown message fields
// (images, tool calls, future additions) must survive untouched.
type Message struct {
	Role    string
	Content string
	Images  []string
}

// ApplySystemMessages applies the shared system-prompt rule to typed
// messages. It is used by the engine; the raw adapter uses PlanSystemPrompt
// directly so it can preserve unknown fields.
func ApplySystemMessages(messages []Message, sp SystemPrompt) []Message {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	prepend, remove := PlanSystemPrompt(roles, sp)
	if !prepend && !remove {
		return messages
	}
	out := make([]Message, 0, len(messages)+1)
	if prepend && strings.TrimSpace(sp.Text) != "" {
		out = append(out, Message{Role: "system", Content: sp.Text})
	}
	for _, message := range messages {
		if remove && isSystemRole(message.Role) {
			continue
		}
		out = append(out, message)
	}
	return out
}

// Empty reports whether the policy would leave messages alone.
func (s SystemPrompt) Empty() bool {
	return s.Mode == SystemPromptOff || (s.Mode == SystemPromptInjectIfMissing && strings.TrimSpace(s.Text) == "")
}

// ParamKind is the JSON shape of a generation parameter.
type ParamKind string

const (
	KindFloat  ParamKind = "float"
	KindInt    ParamKind = "int"
	KindString ParamKind = "string"
	KindBool   ParamKind = "bool"
	KindJSON   ParamKind = "json"
)

// ParamSpec describes one canonical generation parameter and where it goes
// on each API flavour. NativeField is either a top-level key ("keep_alive")
// or an options key ("options.temperature").
type ParamSpec struct {
	Name        string
	V1Field     string
	NativeField string
	Kind        ParamKind
}

// Specs is the canonical parameter table. Adding a parameter here makes it
// available to both the raw proxy and the provider mapping.
var Specs = []ParamSpec{
	{Name: "temperature", V1Field: "temperature", NativeField: "options.temperature", Kind: KindFloat},
	{Name: "top_p", V1Field: "top_p", NativeField: "options.top_p", Kind: KindFloat},
	{Name: "top_k", NativeField: "options.top_k", Kind: KindInt},
	{Name: "max_tokens", V1Field: "max_tokens", NativeField: "options.num_predict", Kind: KindInt},
	{Name: "num_predict", NativeField: "options.num_predict", Kind: KindInt},
	{Name: "num_ctx", NativeField: "options.num_ctx", Kind: KindInt},
	{Name: "num_keep", NativeField: "options.num_keep", Kind: KindInt},
	{Name: "repeat_penalty", NativeField: "options.repeat_penalty", Kind: KindFloat},
	{Name: "repeat_last_n", NativeField: "options.repeat_last_n", Kind: KindInt},
	{Name: "min_p", NativeField: "options.min_p", Kind: KindFloat},
	{Name: "typical_p", NativeField: "options.typical_p", Kind: KindFloat},
	{Name: "presence_penalty", V1Field: "presence_penalty", NativeField: "options.presence_penalty", Kind: KindFloat},
	{Name: "frequency_penalty", V1Field: "frequency_penalty", NativeField: "options.frequency_penalty", Kind: KindFloat},
	{Name: "seed", V1Field: "seed", NativeField: "options.seed", Kind: KindInt},
	{Name: "stop", V1Field: "stop", NativeField: "options.stop", Kind: KindJSON},
	{Name: "keep_alive", NativeField: "keep_alive", Kind: KindString},
	{Name: "format", NativeField: "format", Kind: KindJSON},
}

var specByName = func() map[string]ParamSpec {
	out := make(map[string]ParamSpec, len(Specs))
	for _, spec := range Specs {
		out[spec.Name] = spec
	}
	return out
}()

// Lookup returns the spec for a canonical parameter name.
func Lookup(name string) (ParamSpec, bool) {
	spec, ok := specByName[name]
	return spec, ok
}

// NativePlacement reports the top-level key and options key for a parameter.
// Either may be empty. Unknown parameters stay top-level, matching the
// historical proxy behaviour.
func NativePlacement(name string) (topLevel, option string) {
	spec, ok := specByName[name]
	if !ok {
		return name, ""
	}
	if strings.HasPrefix(spec.NativeField, "options.") {
		return "", strings.TrimPrefix(spec.NativeField, "options.")
	}
	return spec.NativeField, ""
}

// V1Placement reports the OpenAI-compatible top-level field.
func V1Placement(name string) string {
	spec, ok := specByName[name]
	if !ok || spec.V1Field == "" {
		return name
	}
	return spec.V1Field
}

// Policy is a fully resolved set of request rules.
type Policy struct {
	Thinking     ThinkingMode
	SystemPrompt SystemPrompt
	Params       map[string]json.RawMessage
}

// New returns the historical default: thinking off, no system prompt.
func New(params map[string]json.RawMessage) Policy {
	return Policy{
		Thinking:     ThinkingOff,
		SystemPrompt: SystemPrompt{Mode: SystemPromptOff},
		Params:       cloneParams(params),
	}
}

// Clone returns a deep-enough copy: params map is copied, values are immutable
// byte slices by convention.
func (p Policy) Clone() Policy {
	return Policy{
		Thinking:     p.Thinking,
		SystemPrompt: p.SystemPrompt,
		Params:       cloneParams(p.Params),
	}
}

func cloneParams(params map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(params))
	for key, value := range params {
		out[key] = value
	}
	return out
}

// ApplyV1 rewrites an OpenAI-compatible request body.
func ApplyV1(body map[string]json.RawMessage, p Policy) map[string]json.RawMessage {
	out := cloneBody(body)
	applyParamsV1(out, p.Params)
	applyThinkingV1(out, p.Thinking)
	_, _ = ApplySystemPrompt(out, p.SystemPrompt)
	return out
}

// ApplyNative rewrites an Ollama native request body. ok is false when the
// client sent an "options" member that is not an object; the caller must
// pass the request through unchanged in that case.
func ApplyNative(body map[string]json.RawMessage, p Policy) (out map[string]json.RawMessage, ok bool) {
	options, ok := decodeOptions(body["options"])
	if !ok {
		return body, false
	}
	hadOptions := false
	if raw, present := body["options"]; present {
		trimmed := bytes.TrimSpace(raw)
		hadOptions = len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
	}

	out = cloneBody(body)
	changed := applyParamsNative(out, options, p.Params)
	if (hadOptions || changed) && len(options) > 0 {
		raw, err := encodeJSON(options)
		if err != nil {
			return body, false
		}
		out["options"] = raw
	}
	applyThinkingNative(out, p.Thinking)
	_, _ = ApplySystemPrompt(out, p.SystemPrompt)
	return out, true
}

// ApplySystemPrompt injects or overrides a system message in a raw body.
// ok is false when "messages" exists but is not an array of objects; the
// caller should pass the request through untouched.
func ApplySystemPrompt(body map[string]json.RawMessage, sp SystemPrompt) (map[string]json.RawMessage, bool) {
	if sp.Empty() && sp.Mode != SystemPromptOverride {
		return body, true
	}
	raw, present := body["messages"]
	if !present {
		return body, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return body, false
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return body, false
	}
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		role, ok := messageRole(message)
		if !ok {
			return body, false
		}
		roles = append(roles, role)
	}
	prepend, remove := PlanSystemPrompt(roles, sp)
	if !prepend && !remove {
		return body, true
	}
	filtered := messages[:0]
	for index, message := range messages {
		if remove && isSystemRole(roles[index]) {
			continue
		}
		filtered = append(filtered, message)
	}
	if prepend && strings.TrimSpace(sp.Text) != "" {
		system, err := encodeMessage("system", sp.Text)
		if err != nil {
			return body, false
		}
		filtered = append([]json.RawMessage{system}, filtered...)
	}
	encoded, err := encodeJSON(filtered)
	if err != nil {
		return body, false
	}
	body["messages"] = encoded
	return body, true
}

// PlanSystemPrompt is the shared rule used by the raw adapter and the typed
// engine: whether to prepend a system message and whether to remove existing
// system/developer messages.
func PlanSystemPrompt(roles []string, sp SystemPrompt) (prepend, remove bool) {
	if sp.Mode == SystemPromptOff {
		return false, false
	}
	hasSystem := false
	for _, role := range roles {
		if isSystemRole(role) {
			hasSystem = true
			break
		}
	}
	switch sp.Mode {
	case SystemPromptInjectIfMissing:
		if hasSystem || strings.TrimSpace(sp.Text) == "" {
			return false, false
		}
		return true, false
	case SystemPromptOverride:
		return strings.TrimSpace(sp.Text) != "", true
	default:
		return false, false
	}
}

func isSystemRole(role string) bool {
	return role == "system" || role == "developer"
}

func messageRole(raw json.RawMessage) (string, bool) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return "", false
	}
	rawRole, ok := message["role"]
	if !ok {
		return "", false
	}
	var role string
	if err := json.Unmarshal(rawRole, &role); err != nil {
		return "", false
	}
	return role, true
}

// SplitNativeParams turns canonical params into the two places Ollama
// accepts them. The maps are independent copies; max_tokens is skipped when
// num_predict is present because they are aliases.
func SplitNativeParams(params map[string]json.RawMessage) (options, topLevel map[string]json.RawMessage) {
	options = make(map[string]json.RawMessage, len(params))
	topLevel = make(map[string]json.RawMessage, len(params))
	_, hasNumPredict := params["num_predict"]
	for key, value := range params {
		if key == "max_tokens" && hasNumPredict {
			continue
		}
		topKey, option := NativePlacement(key)
		if option != "" {
			if _, present := options[option]; !present {
				options[option] = value
			}
			continue
		}
		if topKey != "" {
			if _, present := topLevel[topKey]; !present {
				topLevel[topKey] = value
			}
		}
	}
	return options, topLevel
}

// applyParamsV1 fills missing top-level fields. Client values always win.
func applyParamsV1(body map[string]json.RawMessage, params map[string]json.RawMessage) {
	for key, value := range params {
		field := V1Placement(key)
		if _, present := body[field]; present {
			continue
		}
		body[field] = value
	}
}

// applyParamsNative fills missing options/top-level fields. Client values
// already present in body/options always win.
func applyParamsNative(body, options map[string]json.RawMessage, params map[string]json.RawMessage) bool {
	newOptions, newTopLevel := SplitNativeParams(params)
	changed := false
	for key, value := range newOptions {
		if _, present := options[key]; present {
			continue
		}
		options[key] = value
		changed = true
	}
	for key, value := range newTopLevel {
		if _, present := body[key]; present {
			continue
		}
		body[key] = value
	}
	return changed
}

func applyThinkingV1(body map[string]json.RawMessage, mode ThinkingMode) {
	switch mode {
	case ThinkingOff:
		body["reasoning_effort"] = json.RawMessage(`"none"`)
		delete(body, "think")
	case ThinkingOn:
		delete(body, "think")
	case ThinkingPassthrough:
		// Leave everything as the client sent it.
	}
}

func applyThinkingNative(body map[string]json.RawMessage, mode ThinkingMode) {
	switch mode {
	case ThinkingOff:
		body["think"] = json.RawMessage("false")
		delete(body, "reasoning_effort")
	case ThinkingOn:
		body["think"] = json.RawMessage("true")
		delete(body, "reasoning_effort")
	case ThinkingPassthrough:
		// Leave everything as the client sent it.
	}
}

func decodeOptions(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	options := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return options, true
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return options, true
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, false
	}
	return options, true
}

func cloneBody(body map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(body)+4)
	for key, value := range body {
		out[key] = value
	}
	return out
}

func encodeMessage(role, content string) (json.RawMessage, error) {
	return encodeJSON(map[string]string{"role": role, "content": content})
}

func encodeJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
