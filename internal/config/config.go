// Package config resolves the proxy configuration from defaults, the
// config file, environment variables and command line flags, and exposes the
// few settings the portal can change at runtime.
//
// Precedence (highest last): built-in defaults < config file < environment
// (PORT / UPSTREAM / PROXY_PARAMS) < CLI flags. Injection params follow the
// same order, with --no-defaults emptying the built-in base first.
//
// The output language is resolved here too (--lang > LLM_WORKBENCH_LANG > config file >
// system locale) and applied to the i18n package, so every message printed
// after Parse comes out in the chosen language — including Parse's own
// errors, because the language is settled before the flags are read.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/policy"
)

const (
	// Version is reported by --version and in the startup banner.
	Version = "0.6.0"

	DefaultUpstream = "http://127.0.0.1:11434"
	DefaultHost     = "127.0.0.1"
	DefaultPort     = 11500
	DefaultTimeout  = 600 * time.Second

	// DefaultMaxConcurrent bounds requests that are in flight at once.
	DefaultMaxConcurrent = 8
	// DefaultMaxBodySize bounds bodies read for JSON injection (1 MiB).
	DefaultMaxBodySize int64 = 1 << 20

	// DefaultInspectorMaxBodyBytes bounds each persisted request/response body.
	DefaultInspectorMaxBodyBytes = 256 << 10
	// DefaultRetentionDays and DefaultMaxRows bound request history.
	DefaultRetentionDays = 14
	DefaultMaxRows       = 5000

	// LegacyDir is the pre-rename application directory. It is only read by
	// the migration path and the first-run fallback; new files never go here.
	LegacyDir = "ollama-nothink-proxy"
)

// InspectorSettings controls request history capture.
type InspectorSettings struct {
	Enabled       bool `json:"enabled"`
	CaptureBodies bool `json:"capture_bodies"`
	MaxBodyBytes  int  `json:"max_body_bytes"`
	RetentionDays int  `json:"retention_days"`
	MaxRows       int  `json:"max_rows"`
}

// StorageSettings points at the local SQLite database.
type StorageSettings struct {
	Path string `json:"path"`
}

// ErrVersionRequested is returned by Parse when --version was given.
var ErrVersionRequested = errors.New("version requested")

var defaultParams = map[string]json.RawMessage{
	"temperature": json.RawMessage("0.2"),
	"top_p":       json.RawMessage("0.9"),
	"max_tokens":  json.RawMessage("96"),
	"keep_alive":  json.RawMessage(`"30m"`),
}

// DefaultParams returns a copy of the built-in injection defaults.
func DefaultParams() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(defaultParams))
	for k, v := range defaultParams {
		out[k] = v
	}
	return out
}

// Snapshot is a consistent copy of the runtime-mutable settings.
type Snapshot struct {
	Upstream     string
	UpstreamURL  *url.URL
	Timeout      time.Duration
	Params       map[string]json.RawMessage
	Lang         i18n.Lang
	Thinking     policy.ThinkingMode
	SystemPrompt policy.SystemPrompt
	Profiles     map[string]policy.Profile
	Profile      string
	Extra        map[string]json.RawMessage
	Inspector    InspectorSettings
	Storage      StorageSettings
}

// Config is the fully resolved runtime configuration.
type Config struct {
	// Startup-only settings (the portal cannot change these).
	Host          string
	Port          int
	ConfigPath    string
	LogPath       string
	Verbose       bool
	MaxConcurrent int
	MaxBodySize   int64
	Foreground    bool
	OpenBrowser   bool

	// Runtime-mutable settings, guarded by mu.
	mu           sync.RWMutex
	upstream     string
	upstreamURL  *url.URL
	timeout      time.Duration
	params       map[string]json.RawMessage
	lang         i18n.Lang
	thinking     policy.ThinkingMode
	systemPrompt policy.SystemPrompt
	profiles     map[string]policy.Profile
	profile      string
	extra        map[string]json.RawMessage
	inspector    InspectorSettings
	storage      StorageSettings
}

// Lang is the language of CLI output, log lines, portal messages and the
// generated plist. The portal can change it at runtime.
func (c *Config) Lang() i18n.Lang {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lang
}

// Upstream returns the upstream URL string.
func (c *Config) Upstream() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.upstream
}

// UpstreamURL returns the parsed upstream address.
func (c *Config) UpstreamURL() *url.URL {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.upstreamURL
}

// Timeout returns the per-operation upstream timeout.
func (c *Config) Timeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.timeout
}

// Params returns a copy of the injection params.
func (c *Config) Params() map[string]json.RawMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneParams(c.params)
}

// Policy resolves the active policy. An empty profile means "the configured
// default"; a non-empty name must exist, otherwise ok is false.
func (c *Config) Policy(profile string) (policy.Policy, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	name := strings.TrimSpace(profile)
	if name == "" {
		name = c.profile
	}
	if name != "" {
		selected, ok := c.profiles[name]
		if !ok {
			return policy.Policy{}, false
		}
		return selected.Policy(), true
	}
	return policy.Policy{
		Thinking:     c.thinking,
		SystemPrompt: c.systemPrompt,
		Params:       cloneParams(c.params),
	}, true
}

// Thinking returns the configured global thinking mode.
func (c *Config) Thinking() policy.ThinkingMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.thinking
}

// SystemPrompt returns the configured global system prompt.
func (c *Config) SystemPrompt() policy.SystemPrompt {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.systemPrompt
}

// ActiveProfile returns the configured default profile name, if any.
func (c *Config) ActiveProfile() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.profile
}

// Inspector returns the history capture settings.
func (c *Config) Inspector() InspectorSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.inspector
}

// Storage returns the database settings.
func (c *Config) Storage() StorageSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.storage
}

// Profiles returns a copy of the configured profiles.
func (c *Config) Profiles() map[string]policy.Profile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneProfiles(c.profiles)
}

// Snapshot returns all mutable settings at once.
func (c *Config) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Upstream:     c.upstream,
		UpstreamURL:  c.upstreamURL,
		Timeout:      c.timeout,
		Params:       cloneParams(c.params),
		Lang:         c.lang,
		Thinking:     c.thinking,
		SystemPrompt: c.systemPrompt,
		Profiles:     cloneProfiles(c.profiles),
		Profile:      c.profile,
		Extra:        cloneParams(c.extra),
		Inspector:    c.inspector,
		Storage:      c.storage,
	}
}

// ListenAddr is the http.Server address.
func (c *Config) ListenAddr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// ListenURL is the address to print in the banner (wildcards become loopback).
func (c *Config) ListenURL() string {
	host := c.Host
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(c.Port))
}

// ParamSummary renders params deterministically for the startup banner.
func (c *Config) ParamSummary() string {
	resolved, ok := c.Policy("")
	if !ok {
		resolved = policy.Policy{Params: c.Params()}
	}
	params := resolved.Params
	if len(params) == 0 {
		return i18n.T("common.none")
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+string(params[k]))
	}
	return strings.Join(parts, ", ")
}

// Document is the JSON shape written to the config file.
func (c *Config) Document() map[string]any {
	snap := c.Snapshot()
	document := make(map[string]any, len(snap.Extra)+9)
	for key, value := range snap.Extra {
		document[key] = value
	}
	document["host"] = c.Host
	document["port"] = c.Port
	document["upstream"] = snap.Upstream
	document["timeout"] = snap.Timeout.Seconds()
	document["params"] = snap.Params
	document["lang"] = snap.Lang.String()
	document["thinking"] = string(snap.Thinking)
	document["system_prompt"] = snap.SystemPrompt
	document["profiles"] = snap.Profiles
	document["profile"] = snap.Profile
	document["inspector"] = snap.Inspector
	document["storage"] = snap.Storage
	return document
}

// Save writes the current configuration to ConfigPath.
func (c *Config) Save() error {
	return writeConfig(c.ConfigPath, c.Document())
}

// Apply validates portal-submitted changes, persists them, then makes them
// live. On error nothing is changed. A "lang" key also switches the runtime
// language (i18n.Set), so portal log lines and the next plist follow the UI.
func (c *Config) Apply(payload map[string]json.RawMessage) error {
	snap := c.Snapshot()
	upstream, timeout, params := snap.Upstream, snap.Timeout, snap.Params
	lang := snap.Lang
	thinking := snap.Thinking
	systemPrompt := snap.SystemPrompt
	profiles := snap.Profiles
	profile := snap.Profile
	extra := snap.Extra
	inspectorSettings := snap.Inspector
	storageSettings := snap.Storage

	if raw, ok := payload["upstream"]; ok {
		value, ok := decodeString(raw)
		if !ok {
			return errors.New(i18n.T("cfg.errUpstreamEmpty"))
		}
		upstream = value
	}
	normalized, parsed, err := normalizeUpstream(upstream)
	if err != nil {
		return err
	}

	if raw, ok := payload["timeout"]; ok {
		seconds, ok := decodeFloat(raw)
		if !ok {
			return fmt.Errorf(i18n.T("cfg.errTimeoutNumber"), bytes.TrimSpace(raw))
		}
		if seconds <= 0 {
			return errors.New(i18n.T("cfg.errTimeoutPositive"))
		}
		timeout = durationFromSeconds(seconds)
	}

	if raw, ok := payload["params"]; ok {
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil || next == nil {
			return errors.New(i18n.T("cfg.errParamsObject"))
		}
		params = next
	}

	if raw, ok := payload["thinking"]; ok {
		value, ok := decodeString(raw)
		mode, valid := policy.ParseThinkingMode(value)
		if !ok || !valid {
			return fmt.Errorf(i18n.T("cfg.errThinking"), bytes.TrimSpace(raw))
		}
		thinking = mode
	}

	if raw, ok := payload["system_prompt"]; ok {
		next, err := parseSystemPrompt(raw)
		if err != nil {
			return err
		}
		systemPrompt = next
	}

	if raw, ok := payload["profiles"]; ok {
		next, err := parseProfiles(raw)
		if err != nil {
			return err
		}
		profiles = mergeProfiles(next)
	}

	if raw, ok := payload["profile"]; ok {
		value, ok := decodeString(raw)
		if !ok {
			return errors.New(i18n.T("cfg.errProfileString"))
		}
		profile = strings.TrimSpace(value)
	}
	if profile != "" {
		if _, ok := profiles[profile]; !ok {
			return fmt.Errorf(i18n.T("cfg.errProfileUnknown"), profile)
		}
	}

	if raw, ok := payload["inspector"]; ok {
		next, err := parseInspector(raw, inspectorSettings)
		if err != nil {
			return err
		}
		inspectorSettings = next
	}
	if raw, ok := payload["storage"]; ok {
		next, err := parseStorage(raw, storageSettings)
		if err != nil {
			return err
		}
		storageSettings = next
	}

	if raw, ok := payload["lang"]; ok {
		value, ok := decodeString(raw)
		if !ok {
			return errors.New(i18n.T("cfg.errLangString"))
		}
		parsedLang, ok := i18n.Parse(value)
		if !ok {
			return errors.New(i18n.T("cfg.errLangString"))
		}
		lang = parsedLang
	}

	// Persist first: a config that cannot be written must not go live.
	document := make(map[string]any, len(extra)+10)
	for key, value := range extra {
		document[key] = value
	}
	document["host"] = c.Host
	document["port"] = c.Port
	document["upstream"] = normalized
	document["timeout"] = timeout.Seconds()
	document["params"] = params
	document["lang"] = lang.String()
	document["thinking"] = string(thinking)
	document["system_prompt"] = systemPrompt
	document["profiles"] = profiles
	document["profile"] = profile
	document["inspector"] = inspectorSettings
	document["storage"] = storageSettings

	// Preserve keys this build does not understand (for example settings
	// written by a newer version). Known keys are normalized above.
	for key, value := range payload {
		switch key {
		case "host", "port", "upstream", "timeout", "params", "lang",
			"thinking", "system_prompt", "profiles", "profile", "inspector", "storage":
			continue
		}
		extra[key] = value
		document[key] = value
	}

	if err := writeConfig(c.ConfigPath, document); err != nil {
		return fmt.Errorf(i18n.T("cfg.errWriteConfig"), err)
	}

	c.mu.Lock()
	c.upstream, c.upstreamURL, c.timeout, c.params, c.lang = normalized, parsed, timeout, params, lang
	c.thinking, c.systemPrompt, c.profiles, c.profile, c.extra = thinking, systemPrompt, profiles, profile, extra
	c.inspector, c.storage = inspectorSettings, storageSettings
	c.mu.Unlock()
	i18n.Set(lang)
	return nil
}

// writeConfig writes the document atomically (temp file + rename).
func writeConfig(path string, document map[string]any) error {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func cloneParams(params map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

func cloneProfiles(profiles map[string]policy.Profile) map[string]policy.Profile {
	out := make(map[string]policy.Profile, len(profiles))
	for name, profile := range profiles {
		out[name] = policy.Profile{
			Thinking:     profile.Thinking,
			SystemPrompt: profile.SystemPrompt,
			Params:       cloneParams(profile.Params),
		}
	}
	return out
}

// DefaultProfiles are the built-in named policies. They are always available
// and can be overridden by the config file.
func DefaultProfiles() map[string]policy.Profile {
	return map[string]policy.Profile{
		"nothink": {
			Thinking:     policy.ThinkingOff,
			SystemPrompt: policy.SystemPrompt{Mode: policy.SystemPromptOff},
			Params:       DefaultParams(),
		},
		"raw": {
			Thinking:     policy.ThinkingPassthrough,
			SystemPrompt: policy.SystemPrompt{Mode: policy.SystemPromptOff},
			Params:       map[string]json.RawMessage{},
		},
		"thinking": {
			Thinking:     policy.ThinkingOn,
			SystemPrompt: policy.SystemPrompt{Mode: policy.SystemPromptOff},
			Params:       map[string]json.RawMessage{},
		},
	}
}

func defaultProfiles() map[string]policy.Profile { return DefaultProfiles() }

func mergeProfiles(stored map[string]policy.Profile) map[string]policy.Profile {
	out := defaultProfiles()
	for name, profile := range stored {
		out[name] = profile
	}
	return out
}

func parseSystemPrompt(raw json.RawMessage) (policy.SystemPrompt, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return policy.SystemPrompt{Mode: policy.SystemPromptOff}, nil
	}
	var payload struct {
		Mode string `json:"mode"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return policy.SystemPrompt{}, errors.New(i18n.T("cfg.errSystemPromptObject"))
	}
	mode := policy.SystemPromptOff
	if payload.Mode != "" {
		parsed, ok := policy.ParseSystemPromptMode(payload.Mode)
		if !ok {
			return policy.SystemPrompt{}, fmt.Errorf(i18n.T("cfg.errSystemPromptMode"), payload.Mode)
		}
		mode = parsed
	}
	return policy.SystemPrompt{Mode: mode, Text: payload.Text}, nil
}

func parseInspector(raw json.RawMessage, current InspectorSettings) (InspectorSettings, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return current, errors.New(i18n.T("cfg.errInspectorObject"))
	}
	next := current
	if value, ok := payload["enabled"]; ok {
		if err := json.Unmarshal(value, &next.Enabled); err != nil {
			return current, errors.New(i18n.T("cfg.errInspectorBool"))
		}
	}
	if value, ok := payload["capture_bodies"]; ok {
		if err := json.Unmarshal(value, &next.CaptureBodies); err != nil {
			return current, errors.New(i18n.T("cfg.errInspectorBool"))
		}
	}
	if value, ok := payload["max_body_bytes"]; ok {
		if err := json.Unmarshal(value, &next.MaxBodyBytes); err != nil {
			return current, errors.New(i18n.T("cfg.errInspectorNumber"))
		}
	}
	if value, ok := payload["retention_days"]; ok {
		if err := json.Unmarshal(value, &next.RetentionDays); err != nil {
			return current, errors.New(i18n.T("cfg.errInspectorNumber"))
		}
	}
	if value, ok := payload["max_rows"]; ok {
		if err := json.Unmarshal(value, &next.MaxRows); err != nil {
			return current, errors.New(i18n.T("cfg.errInspectorNumber"))
		}
	}
	if next.MaxBodyBytes < 0 || next.RetentionDays < 0 || next.MaxRows < 0 {
		return current, errors.New(i18n.T("cfg.errInspectorNegative"))
	}
	if next.MaxBodyBytes == 0 {
		next.MaxBodyBytes = DefaultInspectorMaxBodyBytes
	}
	return next, nil
}

func parseStorage(raw json.RawMessage, current StorageSettings) (StorageSettings, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return current, errors.New(i18n.T("cfg.errStorageObject"))
	}
	next := current
	if value, ok := payload["path"]; ok {
		path, ok := decodeString(value)
		if !ok {
			return current, errors.New(i18n.T("cfg.errStoragePath"))
		}
		next.Path = expandPath(strings.TrimSpace(path))
	}
	if next.Path == "" {
		next.Path = DefaultDataPath()
	}
	return next, nil
}

func parseProfiles(raw json.RawMessage) (map[string]policy.Profile, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, errors.New(i18n.T("cfg.errProfilesObject"))
	}
	out := make(map[string]policy.Profile, len(payload))
	for name, entry := range payload {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New(i18n.T("cfg.errProfileName"))
		}
		var item struct {
			Thinking     string                     `json:"thinking"`
			SystemPrompt json.RawMessage            `json:"system_prompt"`
			Params       map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(entry, &item); err != nil {
			return nil, fmt.Errorf(i18n.T("cfg.errProfileObject"), name)
		}
		thinking := policy.ThinkingOff
		if item.Thinking != "" {
			parsed, ok := policy.ParseThinkingMode(item.Thinking)
			if !ok {
				return nil, fmt.Errorf(i18n.T("cfg.errProfileThinking"), name, item.Thinking)
			}
			thinking = parsed
		}
		systemPrompt := policy.SystemPrompt{Mode: policy.SystemPromptOff}
		if len(bytes.TrimSpace(item.SystemPrompt)) > 0 {
			parsed, err := parseSystemPrompt(item.SystemPrompt)
			if err != nil {
				return nil, fmt.Errorf(i18n.T("cfg.errProfileSystemPrompt"), name, err)
			}
			systemPrompt = parsed
		}
		params := item.Params
		if params == nil {
			params = map[string]json.RawMessage{}
		}
		out[name] = policy.Profile{Thinking: thinking, SystemPrompt: systemPrompt, Params: params}
	}
	return out, nil
}

// extraFrom keeps top-level keys this build does not recognize so a save from
// an older UI (or a downgrade) does not silently drop them.
func extraFrom(raw map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage)
	for key, value := range raw {
		switch key {
		case "host", "port", "upstream", "timeout", "params", "lang",
			"thinking", "system_prompt", "profiles", "profile", "inspector", "storage":
			continue
		}
		out[key] = value
	}
	return out
}

// Parse resolves args (without argv[0]) into a Config.
//
// getenv is injectable so tests do not depend on the ambient environment;
// it must report whether a variable is present (PROXY_PARAMS=” differs from
// an unset PROXY_PARAMS). A nil getenv uses os.LookupEnv.
func Parse(args []string, getenv func(string) (string, bool)) (*Config, error) {
	return parseArgs(args, getenv, false)
}

// ParseRuntime is Parse for the real CLI: when no --config was given and the
// new config file does not exist yet, the pre-rename file is read as a
// fallback. The fallback is read-only; the first Save writes the new path.
func ParseRuntime(args []string, getenv func(string) (string, bool)) (*Config, error) {
	return parseArgs(args, getenv, true)
}

func parseArgs(args []string, getenv func(string) (string, bool), legacyFallback bool) (*Config, error) {
	if getenv == nil {
		getenv = os.LookupEnv
	}

	var (
		host                 string
		port                 int
		portSet              bool
		upstream             string
		sets                 []string
		timeoutSecs          float64
		timeoutSet           bool
		configPath           string
		logPath              string
		foreground           bool
		noBrowser            bool
		noDefaults           bool
		maxConc              int
		maxConcSet           bool
		maxBody              int64
		maxBodySet           bool
		verbose              bool
		showVersion          bool
		langFlag             string
		thinkingFlag         string
		systemPromptFlag     string
		systemPromptFile     string
		systemPromptOverride bool
		profileFlag          string
		noInspector          bool
		captureBodies        bool
		dbPath               string
		retentionDays        int
		retentionSet         bool
		maxRows              int
		maxRowsSet           bool
	)

	// The language has to be settled before the flag parser can complain, so an
	// invalid flag is reported in the language the user asked for. A bad --lang
	// value is reported by the real Parse below, once the flag is known.
	preLang, _ := resolveLang(prescanLang(args), "", getenv)
	i18n.Set(preLang)

	fs := flag.NewFlagSet("llm-workbench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.StringVar(&host, "host", "", "listen address")
	fs.Func("port", "listen port", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf(i18n.T("cfg.errPortFlag"), s)
		}
		port, portSet = v, true
		return nil
	})
	fs.StringVar(&upstream, "upstream", "", "Ollama address")
	fs.Func("set", "override an injected param, repeatable (KEY=VALUE)", func(s string) error {
		sets = append(sets, s)
		return nil
	})
	fs.Func("timeout", "upstream connect/read timeout in seconds", func(s string) error {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf(i18n.T("cfg.errTimeoutFlag"), s)
		}
		timeoutSecs, timeoutSet = v, true
		return nil
	})
	fs.StringVar(&configPath, "config", "", "config file path")
	fs.StringVar(&logPath, "log", "", "log file path")
	fs.StringVar(&langFlag, "lang", "", "output language: zh / en / auto")
	fs.StringVar(&thinkingFlag, "thinking", "", "thinking mode: off / on / passthrough")
	fs.StringVar(&systemPromptFlag, "system-prompt", "", "system prompt to inject when missing")
	fs.StringVar(&systemPromptFile, "system-prompt-file", "", "read the system prompt from a file")
	fs.BoolVar(&systemPromptOverride, "system-prompt-override", false, "replace existing system messages")
	fs.StringVar(&profileFlag, "profile", "", "select a named policy profile")
	fs.BoolVar(&noInspector, "no-inspector", false, "disable request history capture")
	fs.BoolVar(&captureBodies, "capture-bodies", false, "persist request/response bodies in history")
	fs.StringVar(&dbPath, "db", "", "SQLite database path")
	fs.Func("retention-days", "delete history older than N days (0 keeps forever)", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf(i18n.T("cfg.errRetentionFlag"), s)
		}
		retentionDays, retentionSet = v, true
		return nil
	})
	fs.Func("max-rows", "keep at most N history rows (0 keeps all)", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf(i18n.T("cfg.errMaxRowsFlag"), s)
		}
		maxRows, maxRowsSet = v, true
		return nil
	})
	fs.BoolVar(&foreground, "foreground", false, "run in the foreground")
	fs.BoolVar(&foreground, "f", false, "run in the foreground")
	fs.BoolVar(&noBrowser, "no-browser", false, "do not open the portal page on start")
	fs.BoolVar(&noDefaults, "no-defaults", false, "inject no built-in generation params")
	fs.Func("max-concurrent", "max requests in flight", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf(i18n.T("cfg.errConcurrentFlag"), s)
		}
		maxConc, maxConcSet = v, true
		return nil
	})
	fs.Func("max-body-size", "max bytes of a rewritable request body", func(s string) error {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf(i18n.T("cfg.errBodySizeFlag"), s)
		}
		maxBody, maxBodySet = v, true
		return nil
	})
	fs.BoolVar(&verbose, "verbose", false, "print one log line per request")
	fs.BoolVar(&verbose, "v", false, "print one log line per request")
	fs.BoolVar(&showVersion, "version", false, "print the version")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf(i18n.T("cfg.errExtraArgs"), strings.Join(fs.Args(), " "))
	}
	if showVersion {
		return nil, ErrVersionRequested
	}

	explicitConfig := configPath != ""
	configPath = expandPath(configPathOr(configPath))
	logPath = expandPath(logPathOr(logPath))
	stored := loadFile(configPath)
	if legacyFallback && !explicitConfig && !fileExists(configPath) {
		// First run after the rename: honor the old file without copying it.
		// Copying is the explicit "migrate" command's job, so a read-only
		// run never creates files in the new directory.
		stored = loadFile(LegacyConfigPath())
	}

	// CLI flag > LLM_WORKBENCH_LANG > config file > locale. envValue folds LLM_WORKBENCH_LANG in
	// through resolveLang, which reports a bad --lang value as an error.
	lang, err := resolveLang(langFlag, stored.lang, getenv)
	if err != nil {
		return nil, err
	}
	i18n.Set(lang)

	cfg := &Config{
		Host:          firstNonEmpty(host, stored.host, DefaultHost),
		ConfigPath:    configPath,
		LogPath:       logPath,
		Verbose:       verbose,
		MaxConcurrent: DefaultMaxConcurrent,
		MaxBodySize:   DefaultMaxBodySize,
		Foreground:    foreground,
		OpenBrowser:   !noBrowser,
		upstream:      firstNonEmpty(upstream, envValue(getenv, "UPSTREAM"), stored.upstream, DefaultUpstream),
		timeout:       DefaultTimeout,
		lang:          lang,
		thinking:      policy.ThinkingOff,
		systemPrompt:  policy.SystemPrompt{Mode: policy.SystemPromptOff},
		profiles:      defaultProfiles(),
		extra:         extraFrom(stored.raw),
		inspector:     DefaultInspectorSettings(),
		storage:       DefaultStorageSettings(),
	}

	cfg.Port = DefaultPort
	if stored.portSet {
		cfg.Port = stored.port
	}
	if envPort, ok := getenv("PORT"); ok {
		if v, ok := toleranToInt(envPort); ok {
			cfg.Port = v
		}
	}
	if portSet {
		cfg.Port = port
	}

	if stored.timeoutSet {
		cfg.timeout = durationFromSeconds(stored.timeout)
	}
	if timeoutSet {
		cfg.timeout = durationFromSeconds(timeoutSecs)
	}

	if maxConcSet {
		cfg.MaxConcurrent = maxConc
	}
	if maxBodySet {
		cfg.MaxBodySize = maxBody
	}

	params := DefaultParams()
	if noDefaults {
		params = map[string]json.RawMessage{}
	}
	if stored.paramsSet {
		params = stored.params
	}
	if rawEnv, ok := getenv("PROXY_PARAMS"); ok {
		parsed, err := parseParamsObject(rawEnv)
		if err != nil {
			return nil, err
		}
		params = parsed
	}
	for _, item := range sets {
		key, value, err := parseSet(item)
		if err != nil {
			return nil, err
		}
		params[key] = value
	}
	cfg.params = params

	if stored.thinkingSet {
		cfg.thinking = stored.thinking
	}
	if thinkingFlag != "" {
		mode, ok := policy.ParseThinkingMode(thinkingFlag)
		if !ok {
			return nil, fmt.Errorf(i18n.T("cfg.errThinking"), thinkingFlag)
		}
		cfg.thinking = mode
	}

	if stored.systemPromptSet {
		cfg.systemPrompt = stored.systemPrompt
	}
	if systemPromptFlag != "" && systemPromptFile != "" {
		return nil, errors.New(i18n.T("cfg.errSystemPromptBoth"))
	}
	if systemPromptFile != "" {
		data, err := os.ReadFile(expandPath(systemPromptFile))
		if err != nil {
			return nil, fmt.Errorf(i18n.T("cfg.errSystemPromptFile"), systemPromptFile, err)
		}
		if len(data) > 1<<20 {
			return nil, fmt.Errorf(i18n.T("cfg.errSystemPromptFile"), systemPromptFile, errors.New("file is larger than 1 MiB"))
		}
		cfg.systemPrompt = policy.SystemPrompt{Mode: policy.SystemPromptInjectIfMissing, Text: string(data)}
	}
	if systemPromptFlag != "" {
		cfg.systemPrompt = policy.SystemPrompt{Mode: policy.SystemPromptInjectIfMissing, Text: systemPromptFlag}
	}
	if systemPromptOverride {
		cfg.systemPrompt.Mode = policy.SystemPromptOverride
	}

	if stored.profilesSet {
		cfg.profiles = mergeProfiles(stored.profiles)
	}
	profile := strings.TrimSpace(firstNonEmpty(profileFlag, stored.profile))
	if profile != "" {
		selected, ok := cfg.profiles[profile]
		if !ok {
			return nil, fmt.Errorf(i18n.T("cfg.errProfileUnknown"), profile)
		} // An explicit --set still wins over a profile, otherwise the flag
		// would be silently ignored when a profile is selected.
		for _, item := range sets {
			key, value, err := parseSet(item)
			if err != nil {
				return nil, err
			}
			selected.Params[key] = value
		}
		// Explicit thinking / system-prompt flags also win over the profile.
		if thinkingFlag != "" {
			selected.Thinking = cfg.thinking
		}
		if systemPromptFlag != "" || systemPromptFile != "" || systemPromptOverride {
			selected.SystemPrompt = cfg.systemPrompt
		}
		cfg.profiles[profile] = selected
		cfg.profile = profile
	}

	if stored.inspectorSet {
		cfg.inspector = stored.inspector
	}
	if stored.storageSet {
		cfg.storage = stored.storage
	}
	if dbPath != "" {
		cfg.storage.Path = expandPath(dbPath)
	}
	if noInspector {
		cfg.inspector.Enabled = false
	}
	if captureBodies {
		cfg.inspector.CaptureBodies = true
	}
	if retentionSet {
		cfg.inspector.RetentionDays = retentionDays
	}
	if maxRowsSet {
		cfg.inspector.MaxRows = maxRows
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// resolveLang picks the runtime language: an explicit --lang beats LLM_WORKBENCH_LANG,
// which beats the config file, which beats the locale. Only a bad --lang value
// is an error; a bad environment or file value just falls through.
func resolveLang(flagValue, stored string, getenv func(string) (string, bool)) (i18n.Lang, error) {
	if flagValue != "" && !strings.EqualFold(flagValue, i18n.Auto) {
		lang, ok := i18n.Parse(flagValue)
		if !ok {
			return i18n.ZH, fmt.Errorf(i18n.T("cfg.errLangValue"), flagValue)
		}
		return lang, nil
	}
	if flagValue == "" {
		if env, ok := lookupLangEnv(getenv); ok && !strings.EqualFold(strings.TrimSpace(env), i18n.Auto) {
			if lang, ok := i18n.Parse(env); ok {
				return lang, nil
			}
		}
		if lang, ok := i18n.Parse(stored); ok {
			return lang, nil
		}
	}
	return i18n.Detect(getenv), nil
}

// lookupLangEnv reads the new language variable first, then the pre-rename
// ONP_LANG alias, so existing launch agents and shell profiles keep working.
func lookupLangEnv(getenv func(string) (string, bool)) (string, bool) {
	if value, ok := getenv("LLM_WORKBENCH_LANG"); ok {
		return value, true
	}
	return getenv("ONP_LANG")
}

// prescanLang reads --lang out of args before the flag parser runs, so even a
// flag error can be reported in the requested language.
func prescanLang(args []string) string {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if value, ok := strings.CutPrefix(arg, "--lang="); ok {
			return value
		}
		if arg == "--lang" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

// envValue returns key's value, or "" when unset.
func envValue(getenv func(string) (string, bool), key string) string {
	v, _ := getenv(key)
	return v
}

func (c *Config) validate() error {
	normalized, parsed, err := normalizeUpstream(c.upstream)
	if err != nil {
		return err
	}
	c.upstream, c.upstreamURL = normalized, parsed

	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf(i18n.T("cfg.errPortRange"), c.Port)
	}
	if c.timeout <= 0 {
		return errors.New(i18n.T("cfg.errTimeoutPositive"))
	}
	if c.MaxConcurrent < 1 {
		return errors.New(i18n.T("cfg.errMaxConcurrent"))
	}
	if c.MaxBodySize < 1 {
		return errors.New(i18n.T("cfg.errMaxBodySize"))
	}
	if c.params == nil {
		c.params = map[string]json.RawMessage{}
	}
	if c.storage.Path == "" {
		c.storage.Path = DefaultDataPath()
	}
	c.storage.Path = expandPath(c.storage.Path)
	if c.inspector.MaxBodyBytes < 0 || c.inspector.RetentionDays < 0 || c.inspector.MaxRows < 0 {
		return errors.New(i18n.T("cfg.errInspectorNegative"))
	}
	if c.inspector.MaxBodyBytes == 0 {
		c.inspector.MaxBodyBytes = DefaultInspectorMaxBodyBytes
	}
	return nil
}

func normalizeUpstream(raw string) (string, *url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil, errors.New(i18n.T("cfg.errUpstreamEmpty"))
	}
	value = strings.TrimRight(value, "/")
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return "", nil, fmt.Errorf(i18n.T("cfg.errUpstreamScheme"), raw)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", nil, fmt.Errorf(i18n.T("cfg.errUpstreamURL"), raw)
	}
	// Only scheme://host is forwarded: a base path on the upstream is dropped
	// so /api/* and /v1/* always hit Ollama's root routes.
	parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", "", ""
	return value, parsed, nil
}

// Coerce turns a --set string into a JSON value: valid JSON stays JSON,
// everything else becomes a string.
func Coerce(value string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	if json.Valid([]byte(trimmed)) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(trimmed)); err == nil {
			return json.RawMessage(buf.Bytes())
		}
		return json.RawMessage(trimmed)
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(encoded)
}

func parseSet(item string) (string, json.RawMessage, error) {
	key, value, ok := strings.Cut(item, "=")
	if !ok {
		return "", nil, fmt.Errorf(i18n.T("cfg.errSetForm"), item)
	}
	return strings.TrimSpace(key), Coerce(strings.TrimSpace(value)), nil
}

// parseParamsObject parses PROXY_PARAMS. An empty value means "{}" (inject
// nothing), a non-object is an error.
func parseParamsObject(raw string) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]json.RawMessage{}, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return nil, fmt.Errorf(i18n.T("cfg.errParamsEnv"), raw)
	}
	return out, nil
}

type fileConfig struct {
	host            string
	port            int
	portSet         bool
	upstream        string
	timeout         float64
	timeoutSet      bool
	params          map[string]json.RawMessage
	paramsSet       bool
	lang            string
	thinking        policy.ThinkingMode
	thinkingSet     bool
	systemPrompt    policy.SystemPrompt
	systemPromptSet bool
	profiles        map[string]policy.Profile
	profilesSet     bool
	profile         string
	inspector       InspectorSettings
	inspectorSet    bool
	storage         StorageSettings
	storageSet      bool
	raw             map[string]json.RawMessage
}

// loadFile reads the JSON config file. A missing or corrupt file is treated
// as empty so built-in defaults still apply.
func loadFile(path string) fileConfig {
	var fc fileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return fc
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return fc
	}
	fc.raw = raw
	if v, ok := raw["host"]; ok {
		if s, ok := decodeString(v); ok {
			fc.host = s
		}
	}
	if v, ok := raw["port"]; ok {
		if n, ok := toleranToInt(string(bytes.TrimSpace(v))); ok {
			fc.port, fc.portSet = n, true
		}
	}
	if v, ok := raw["upstream"]; ok {
		if s, ok := decodeString(v); ok {
			fc.upstream = s
		}
	}
	if v, ok := raw["timeout"]; ok {
		if f, ok := decodeFloat(v); ok {
			fc.timeout, fc.timeoutSet = f, true
		}
	}
	if v, ok := raw["params"]; ok {
		var params map[string]json.RawMessage
		if err := json.Unmarshal(v, &params); err == nil && params != nil {
			fc.params, fc.paramsSet = params, true
		}
	}
	if v, ok := raw["lang"]; ok {
		if s, ok := decodeString(v); ok {
			fc.lang = s
		}
	}
	if v, ok := raw["thinking"]; ok {
		if s, ok := decodeString(v); ok {
			if mode, valid := policy.ParseThinkingMode(s); valid {
				fc.thinking, fc.thinkingSet = mode, true
			}
		}
	}
	if v, ok := raw["system_prompt"]; ok {
		if sp, err := parseSystemPrompt(v); err == nil {
			fc.systemPrompt, fc.systemPromptSet = sp, true
		}
	}
	if v, ok := raw["profiles"]; ok {
		if profiles, err := parseProfiles(v); err == nil {
			fc.profiles, fc.profilesSet = profiles, true
		}
	}
	if v, ok := raw["profile"]; ok {
		if s, ok := decodeString(v); ok {
			fc.profile = s
		}
	}
	if v, ok := raw["inspector"]; ok {
		if settings, err := parseInspector(v, DefaultInspectorSettings()); err == nil {
			fc.inspector, fc.inspectorSet = settings, true
		}
	}
	if v, ok := raw["storage"]; ok {
		if settings, err := parseStorage(v, DefaultStorageSettings()); err == nil {
			fc.storage, fc.storageSet = settings, true
		}
	}
	return fc
}

func decodeString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func decodeFloat(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err == nil {
		return f, true
	}
	// Tolerate a numeric string, e.g. "600" for 600.
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func toleranToInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if v, err := strconv.Atoi(raw); err == nil {
		return v, true
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f == float64(int(f)) {
		return int(f), true
	}
	if s, err := strconv.Unquote(raw); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v, true
		}
	}
	return 0, false
}

func durationFromSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func configPathOr(cli string) string {
	if cli != "" {
		return cli
	}
	return DefaultConfigPath()
}

func logPathOr(cli string) string {
	if cli != "" {
		return cli
	}
	return DefaultLogPath()
}

func expandPath(path string) string {
	if path == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(path, "~"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// DefaultConfigPath is where the config file lives when --config is omitted.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "llm-workbench", "config.json")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "llm-workbench", "config.json")
}

// LegacyConfigPath is the pre-rename config location. It is only read during
// migration and the first-run fallback.
func LegacyConfigPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", LegacyDir, "config.json")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, LegacyDir, "config.json")
}

// ConfigPathFromArgs returns the --config value without parsing any other
// flag. It is used by "migrate" before the full parser runs.
func ConfigPathFromArgs(args []string) string {
	for index := 0; index < len(args); index++ {
		if value, ok := strings.CutPrefix(args[index], "--config="); ok {
			return value
		}
		if args[index] == "--config" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

// ConfigMigration describes what ImportLegacyConfig did.
type ConfigMigration struct {
	Source      string
	Destination string
	Copied      bool
	// Reason is "copied", "destination_exists" or "source_missing".
	Reason string
}

// ImportLegacyConfig copies the pre-rename config to dst when dst is missing.
// The old file is never removed. A malformed source is an error, so a broken
// JSON file is reported instead of being copied silently.
func ImportLegacyConfig(dst string) (ConfigMigration, error) {
	source := LegacyConfigPath()
	result := ConfigMigration{Source: source, Destination: dst}
	if dst == "" {
		dst = DefaultConfigPath()
	}
	dst = expandPath(dst)
	result.Destination = dst
	if _, err := os.Stat(dst); err == nil {
		result.Reason = "destination_exists"
		return result, nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			result.Reason = "source_missing"
			return result, nil
		}
		return result, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return result, fmt.Errorf("legacy config is not a JSON object: %s", source)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(source); err == nil {
		mode = info.Mode().Perm()
	}
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return result, err
		}
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return result, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return result, err
	}
	result.Copied = true
	result.Reason = "copied"
	return result, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DefaultLogPath is where request logs live when --log is omitted.
func DefaultLogPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Logs", "llm-workbench.log")
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "llm-workbench", "llm-workbench.log")
}

// DefaultDataPath is the SQLite database used by the workbench.
func DefaultDataPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "llm-workbench", "workbench.db")
	}
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "llm-workbench", "workbench.db")
		}
		return filepath.Join(home, "AppData", "Local", "llm-workbench", "workbench.db")
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "llm-workbench", "workbench.db")
}

// DefaultInspectorSettings enables metadata capture but not body capture.
func DefaultInspectorSettings() InspectorSettings {
	return InspectorSettings{
		Enabled:       true,
		CaptureBodies: false,
		MaxBodyBytes:  DefaultInspectorMaxBodyBytes,
		RetentionDays: DefaultRetentionDays,
		MaxRows:       DefaultMaxRows,
	}
}

// DefaultStorageSettings uses the platform data directory.
func DefaultStorageSettings() StorageSettings {
	return StorageSettings{Path: DefaultDataPath()}
}

// Commands accepted as the first argument. Everything after it is parsed as
// normal flags, so `restart --port 11501` restarts on another port.
const (
	// CommandServe starts the proxy (the default when no command is given).
	CommandServe = "serve"
	// CommandRestart stops the running instance and starts a fresh one.
	CommandRestart = "restart"
	// CommandMigrate copies the pre-rename config to the new location.
	CommandMigrate = "migrate"
)

// SplitCommand pulls an optional leading subcommand off args. Anything it does
// not recognise is left in place so the flag parser reports it.
func SplitCommand(args []string) (command string, rest []string) {
	if len(args) > 0 {
		switch args[0] {
		case CommandRestart:
			return CommandRestart, args[1:]
		case CommandMigrate:
			return CommandMigrate, args[1:]
		case CommandServe, "start":
			return CommandServe, args[1:]
		}
	}
	return CommandServe, args
}

// UsageText is printed for --help, in the language Parse settled on.
func UsageText() string {
	return i18n.T("help.usage",
		DefaultHost,
		strconv.Itoa(DefaultPort),
		DefaultUpstream,
		strconv.Itoa(int(DefaultTimeout.Seconds())),
		strconv.Itoa(DefaultMaxConcurrent),
		strconv.FormatInt(DefaultMaxBodySize, 10),
		DefaultConfigPath(),
		DefaultLogPath(),
		DefaultDataPath(),
		strconv.Itoa(DefaultRetentionDays),
		strconv.Itoa(DefaultMaxRows),
	)
}
