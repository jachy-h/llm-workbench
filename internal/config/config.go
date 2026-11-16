// Package config resolves the proxy configuration from built-in defaults, the
// config file and the small set of CLI bootstrap flags, and exposes the
// settings the portal can change at runtime.
//
// Precedence (highest last): built-in defaults < config file < CLI flags.
// Injection params follow the same order; --set overlays the resolved base.
//
// The output language is resolved here too (config file > system locale) and
// applied to the i18n package, so every message printed after Parse comes out
// in the chosen language — including Parse's own errors, because the language
// is settled before the flags are read.
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
	// DefaultPort is the listen port. It is deliberately not 11434: that is
	// Ollama's port (the default upstream), so listening there can only fail
	// with "address already in use".
	DefaultPort    = 11500
	DefaultTimeout = 600 * time.Second

	// OllamaPort is the port the default upstream listens on. The proxy never
	// listens there, not even when an old config file says so.
	OllamaPort = 11434

	// DefaultMaxConcurrent bounds requests that are in flight at once.
	DefaultMaxConcurrent = 8
	// DefaultMaxBodySize bounds bodies read for JSON injection (1 MiB).
	DefaultMaxBodySize int64 = 1 << 20

	// DefaultInspectorMaxBodyBytes bounds each persisted request/response body.
	DefaultInspectorMaxBodyBytes = 256 << 10
	// DefaultRetentionDays and DefaultMaxRows bound request history.
	DefaultRetentionDays = 14
	DefaultMaxRows       = 5000
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
	Upstream      string
	UpstreamURL   *url.URL
	Timeout       time.Duration
	Params        map[string]json.RawMessage
	Lang          i18n.Lang
	Thinking      policy.ThinkingMode
	SystemPrompt  policy.SystemPrompt
	Profiles      map[string]policy.Profile
	Profile       string
	Extra         map[string]json.RawMessage
	Inspector     InspectorSettings
	Storage       StorageSettings
	MaxConcurrent int
	MaxBodySize   int64
}

// Config is the fully resolved runtime configuration.
type Config struct {
	// Startup-only settings (the portal cannot change these).
	Host        string
	Port        int
	ConfigPath  string
	LogPath     string
	Foreground  bool
	OpenBrowser bool
	// MigratedFrom is the legacy config file this run imported, empty when
	// there was nothing to migrate.
	MigratedFrom string
	// PortRequested is the listen port the file or the flag asked for when it
	// could not be used (Ollama's port, or the upstream's own); Port holds the
	// port that is used instead.
	PortRequested int

	// Runtime-mutable settings, guarded by mu.
	mu            sync.RWMutex
	upstream      string
	upstreamURL   *url.URL
	timeout       time.Duration
	params        map[string]json.RawMessage
	lang          i18n.Lang
	thinking      policy.ThinkingMode
	systemPrompt  policy.SystemPrompt
	profiles      map[string]policy.Profile
	profile       string
	extra         map[string]json.RawMessage
	inspector     InspectorSettings
	storage       StorageSettings
	maxConcurrent int
	maxBodySize   int64
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

// MaxConcurrent bounds requests in flight.
func (c *Config) MaxConcurrent() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxConcurrent
}

// MaxBodySize bounds bodies read for JSON injection.
func (c *Config) MaxBodySize() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxBodySize
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
		Upstream:      c.upstream,
		UpstreamURL:   c.upstreamURL,
		Timeout:       c.timeout,
		Params:        cloneParams(c.params),
		Lang:          c.lang,
		Thinking:      c.thinking,
		SystemPrompt:  c.systemPrompt,
		Profiles:      cloneProfiles(c.profiles),
		Profile:       c.profile,
		Extra:         cloneParams(c.extra),
		Inspector:     c.inspector,
		Storage:       c.storage,
		MaxConcurrent: c.maxConcurrent,
		MaxBodySize:   c.maxBodySize,
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
	document := make(map[string]any, len(snap.Extra)+11)
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
	document["max_concurrent"] = snap.MaxConcurrent
	document["max_body_size"] = snap.MaxBodySize
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
	maxConcurrent := snap.MaxConcurrent
	maxBodySize := snap.MaxBodySize

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

	if raw, ok := payload["max_concurrent"]; ok {
		value, ok := decodeInt(raw)
		if !ok || value < 1 {
			return fmt.Errorf(i18n.T("cfg.errMaxConcurrentValue"), bytes.TrimSpace(raw))
		}
		maxConcurrent = value
	}
	if raw, ok := payload["max_body_size"]; ok {
		value, ok := decodeFloat(raw)
		if !ok || value < 1 {
			return fmt.Errorf(i18n.T("cfg.errMaxBodySizeValue"), bytes.TrimSpace(raw))
		}
		maxBodySize = int64(value)
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
	document["max_concurrent"] = maxConcurrent
	document["max_body_size"] = maxBodySize

	// Preserve keys this build does not understand (for example settings
	// written by a newer version). Known keys are normalized above.
	for key, value := range payload {
		switch key {
		case "host", "port", "upstream", "timeout", "params", "lang",
			"thinking", "system_prompt", "profiles", "profile", "inspector", "storage",
			"max_concurrent", "max_body_size":
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
	c.maxConcurrent, c.maxBodySize = maxConcurrent, maxBodySize
	c.mu.Unlock()
	i18n.Set(lang)
	return nil
}

// writeConfig writes the document atomically (temp file + rename). The file is
// plain JSON, which every JSONC reader (including this one) accepts.
func writeConfig(path string, document map[string]any) error {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeConfigBytes(path, encoded)
}

// writeConfigBytes writes already-encoded bytes atomically, creating the
// directory when it does not exist yet.
func writeConfigBytes(path string, encoded []byte) error {
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
			"thinking", "system_prompt", "profiles", "profile", "inspector", "storage",
			"max_concurrent", "max_body_size":
			continue
		}
		out[key] = value
	}
	return out
}

// Parse resolves args (without argv[0]) into a Config.
//
// getenv is injectable so tests can pin the system locale; it is only used to
// detect the language when the config file has none. A nil getenv uses
// os.LookupEnv.
func Parse(args []string, getenv func(string) (string, bool)) (*Config, error) {
	return parseArgs(args, getenv)
}

// ParseRuntime is Parse for the real CLI.
func ParseRuntime(args []string, getenv func(string) (string, bool)) (*Config, error) {
	return parseArgs(args, getenv)
}

func parseArgs(args []string, getenv func(string) (string, bool)) (*Config, error) {
	if getenv == nil {
		getenv = os.LookupEnv
	}
	// A flag error can happen before the config file is read, so settle the
	// locale language first; the stored choice replaces it once --config is
	// known.
	i18n.Set(i18n.Detect(getenv))

	var (
		host        string
		port        int
		portSet     bool
		configPath  string
		logPath     string
		dbPath      string
		sets        []string
		foreground  bool
		noBrowser   bool
		showVersion bool
	)

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
	fs.Func("set", "override an injected param, repeatable (KEY=VALUE)", func(s string) error {
		sets = append(sets, s)
		return nil
	})
	fs.StringVar(&configPath, "config", "", "config file path")
	fs.StringVar(&logPath, "log", "", "log file path")
	fs.StringVar(&dbPath, "db", "", "SQLite database path")
	fs.BoolVar(&showVersion, "version", false, "print the version")
	fs.BoolVar(&showVersion, "v", false, "print the version")
	// Internal flags: the background daemon and the LaunchAgent need a
	// process that stays attached and must not open a browser. They are not
	// part of the documented CLI and are deliberately absent from --help.
	fs.BoolVar(&foreground, "foreground", false, "internal: run in the foreground")
	fs.BoolVar(&foreground, "f", false, "internal: run in the foreground")
	fs.BoolVar(&noBrowser, "no-browser", false, "internal: do not open the page")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf(i18n.T("cfg.errExtraArgs"), strings.Join(fs.Args(), " "))
	}
	if showVersion {
		return nil, ErrVersionRequested
	}

	// Only the default config path migrates: an explicit --config is taken
	// exactly as given.
	defaultConfig := configPath == ""
	configPath = expandPath(configPathOr(configPath))
	logPath = expandPath(logPathOr(logPath))
	migratedFrom := ""
	if defaultConfig {
		migratedFrom = migrateConfig(configPath)
	}
	stored := loadFile(configPath)

	lang := resolveLang(stored.lang, getenv)
	i18n.Set(lang)

	cfg := &Config{
		Host:          firstNonEmpty(host, stored.host, DefaultHost),
		ConfigPath:    configPath,
		LogPath:       logPath,
		Foreground:    foreground,
		OpenBrowser:   !noBrowser,
		upstream:      firstNonEmpty(stored.upstream, DefaultUpstream),
		timeout:       DefaultTimeout,
		params:        DefaultParams(),
		lang:          lang,
		thinking:      policy.ThinkingOff,
		systemPrompt:  policy.SystemPrompt{Mode: policy.SystemPromptOff},
		profiles:      defaultProfiles(),
		MigratedFrom:  migratedFrom,
		extra:         extraFrom(stored.raw),
		inspector:     DefaultInspectorSettings(),
		storage:       DefaultStorageSettings(),
		maxConcurrent: DefaultMaxConcurrent,
		maxBodySize:   DefaultMaxBodySize,
	}

	cfg.Port = DefaultPort
	if stored.portSet {
		cfg.Port = stored.port
	}
	if portSet {
		cfg.Port = port
	}

	if stored.timeoutSet {
		cfg.timeout = durationFromSeconds(stored.timeout)
	}
	if stored.maxConcurrentSet {
		cfg.maxConcurrent = stored.maxConcurrent
	}
	if stored.maxBodySizeSet {
		cfg.maxBodySize = stored.maxBodySize
	}

	if stored.paramsSet {
		cfg.params = stored.params
	}
	for _, item := range sets {
		key, value, err := parseSet(item)
		if err != nil {
			return nil, err
		}
		cfg.params[key] = value
	}

	if stored.thinkingSet {
		cfg.thinking = stored.thinking
	}
	if stored.systemPromptSet {
		cfg.systemPrompt = stored.systemPrompt
	}
	if stored.profilesSet {
		cfg.profiles = mergeProfiles(stored.profiles)
	}
	profile := strings.TrimSpace(stored.profile)
	if profile != "" {
		selected, ok := cfg.profiles[profile]
		if !ok {
			return nil, fmt.Errorf(i18n.T("cfg.errProfileUnknown"), profile)
		}
		// An explicit --set still wins over the selected profile, otherwise
		// the flag would be silently ignored whenever a profile is active.
		for _, item := range sets {
			key, value, err := parseSet(item)
			if err != nil {
				return nil, err
			}
			selected.Params[key] = value
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

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	// A listen port that cannot work is corrected instead of left to fail at
	// bind time.
	if fixed, changed := fixPort(cfg.Port, cfg.upstreamURL); changed {
		cfg.PortRequested = cfg.Port
		cfg.Port = fixed
		if !portSet {
			// The port came from the file: persist the correction so the next
			// start does not have to fix it again. A failed write is harmless,
			// this run already uses the fixed value.
			_ = cfg.Save()
		}
	}
	return cfg, nil
}

// resolveLang picks the runtime language: the config file beats the system
// locale. There is no language environment variable; the portal stores the
// choice in the config file.
func resolveLang(stored string, getenv func(string) (string, bool)) i18n.Lang {
	if lang, ok := i18n.Parse(stored); ok {
		return lang
	}
	return i18n.Detect(getenv)
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
	if c.maxConcurrent < 1 {
		return errors.New(i18n.T("cfg.errMaxConcurrent"))
	}
	if c.maxBodySize < 1 {
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

// fixPort keeps the proxy off ports it cannot own: Ollama's port and the port
// the upstream itself listens on (forwarding to yourself is a loop). It
// returns the port to listen on and whether it had to change.
func fixPort(port int, upstream *url.URL) (int, bool) {
	if port <= 0 || port > 65535 {
		return port, false // validate reports out-of-range ports
	}
	if port != OllamaPort && port != upstreamPort(upstream) {
		return port, false
	}
	if DefaultPort == upstreamPort(upstream) {
		return port, false // no better port to offer
	}
	return DefaultPort, true
}

// upstreamPort is the port the upstream URL connects to, with the scheme
// default (80/443) when the URL has none.
func upstreamPort(upstream *url.URL) int {
	if upstream == nil {
		return 0
	}
	if p := upstream.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if upstream.Scheme == "https" {
		return 443
	}
	return 80
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

type fileConfig struct {
	host             string
	port             int
	portSet          bool
	upstream         string
	timeout          float64
	timeoutSet       bool
	params           map[string]json.RawMessage
	paramsSet        bool
	lang             string
	thinking         policy.ThinkingMode
	thinkingSet      bool
	systemPrompt     policy.SystemPrompt
	systemPromptSet  bool
	profiles         map[string]policy.Profile
	profilesSet      bool
	profile          string
	inspector        InspectorSettings
	inspectorSet     bool
	storage          StorageSettings
	storageSet       bool
	maxConcurrent    int
	maxConcurrentSet bool
	maxBodySize      int64
	maxBodySizeSet   bool
	raw              map[string]json.RawMessage
}

// loadFile reads the JSONC config file. A missing or corrupt file is treated
// as empty so built-in defaults still apply.
func loadFile(path string) fileConfig {
	var fc fileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return fc
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &raw); err != nil || raw == nil {
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
	if v, ok := raw["max_concurrent"]; ok {
		if n, ok := decodeInt(v); ok && n > 0 {
			fc.maxConcurrent, fc.maxConcurrentSet = n, true
		}
	}
	if v, ok := raw["max_body_size"]; ok {
		if n, ok := decodeInt(v); ok && n > 0 {
			fc.maxBodySize, fc.maxBodySizeSet = int64(n), true
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

func decodeInt(raw json.RawMessage) (int, bool) {
	value, ok := decodeFloat(raw)
	if !ok || value != float64(int(value)) {
		return 0, false
	}
	return int(value), true
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

// ConfigDir is where the config file lives: ~/.config/llm-workbench on every
// platform (XDG_CONFIG_HOME wins when it is set). One path for all systems
// keeps the docs, the autostart plist and the user's habits identical.
func ConfigDir() string {
	if base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); base != "" {
		return filepath.Join(expandPath(base), "llm-workbench")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "llm-workbench")
}

// DefaultConfigPath is where the config file lives when --config is omitted.
// The .jsonc suffix is the promise that comments and trailing commas are fine.
func DefaultConfigPath() string {
	return filepath.Join(ConfigDir(), "config.jsonc")
}

// ConfigBackupSuffix is appended to a legacy file once it has been migrated,
// so the import runs once and the original stays recoverable.
const ConfigBackupSuffix = ".bak"

// LegacyConfigPaths lists the config files older releases used, in the order
// they are tried. The first one that exists is imported into the new path.
func LegacyConfigPaths() []string {
	home, _ := os.UserHomeDir()
	support := filepath.Join(home, "Library", "Application Support")
	return []string{
		// The pre-JSONC layout in the current directory.
		filepath.Join(ConfigDir(), "config.json"),
		// macOS used Application Support up to 0.6.
		filepath.Join(support, "llm-workbench", "config.json"),
		// The project used to be called ollama-nothink-proxy.
		filepath.Join(support, "ollama-nothink-proxy", "config.json"),
	}
}

// migrateConfig imports the first legacy config file into path and returns
// the source it used, or "" when there was nothing to migrate. An existing
// config at path always wins: a user who already moved on is not overwritten.
func migrateConfig(path string) string {
	if _, err := os.Stat(path); err == nil {
		return ""
	}
	for _, legacy := range LegacyConfigPaths() {
		if filepath.Clean(legacy) == filepath.Clean(path) {
			continue
		}
		if info, err := os.Stat(legacy); err != nil || info.IsDir() {
			continue
		}
		if err := migrateFile(legacy, path); err != nil {
			continue
		}
		return legacy
	}
	return ""
}

// migrateFile re-encodes the legacy document at path, correcting a listen
// port that cannot work, then keeps the source as ".bak".
func migrateFile(legacy, path string) error {
	data, err := os.ReadFile(legacy)
	if err != nil {
		return err
	}
	document := map[string]json.RawMessage{}
	if stripped := stripJSONC(data); len(bytes.TrimSpace(stripped)) > 0 {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(stripped, &decoded); err != nil {
			return err
		}
		if decoded != nil {
			document = decoded
		}
	}
	if raw, ok := document["port"]; ok {
		if port, ok := toleranToInt(string(bytes.TrimSpace(raw))); ok {
			if fixed, changed := fixPort(port, upstreamFromDocument(document)); changed {
				document["port"] = json.RawMessage(strconv.Itoa(fixed))
			}
		}
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := writeConfigBytes(path, encoded); err != nil {
		return err
	}
	return os.Rename(legacy, legacy+ConfigBackupSuffix)
}

// upstreamFromDocument reads the upstream URL out of a not-yet-validated
// config document; a missing or broken value means "the default upstream".
func upstreamFromDocument(document map[string]json.RawMessage) *url.URL {
	raw, ok := document["upstream"]
	if !ok {
		parsed, _ := url.Parse(DefaultUpstream)
		return parsed
	}
	value, ok := decodeString(raw)
	if !ok {
		return nil
	}
	_, parsed, err := normalizeUpstream(value)
	if err != nil {
		return nil
	}
	return parsed
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
	// "serve" is kept as an undocumented alias for "start".
	CommandServe = "serve"
	// CommandStop asks the running instance to shut down.
	CommandStop = "stop"
	// CommandRestart stops the running instance and starts a fresh one.
	CommandRestart = "restart"
)

// SplitCommand pulls an optional leading subcommand off args. Anything it does
// not recognise is left in place so the flag parser reports it.
func SplitCommand(args []string) (command string, rest []string) {
	if len(args) > 0 {
		switch args[0] {
		case CommandStop:
			return CommandStop, args[1:]
		case CommandRestart:
			return CommandRestart, args[1:]
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
		DefaultConfigPath(),
		DefaultLogPath(),
		DefaultDataPath(),
	)
}
