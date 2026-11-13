// Package config resolves the proxy configuration from defaults, the
// config file, environment variables and command line flags, and exposes the
// few settings the portal can change at runtime.
//
// Precedence (highest last): built-in defaults < config file < environment
// (PORT / UPSTREAM / PROXY_PARAMS) < CLI flags. Injection params follow the
// same order, with --no-defaults emptying the built-in base first.
//
// The output language is resolved here too (--lang > ONP_LANG > config file >
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

	"github.com/jachy-h/ollama-nothink-proxy/internal/i18n"
)

const (
	// Version is reported by --version and in the startup banner.
	Version = "0.2.0"

	DefaultUpstream = "http://127.0.0.1:11434"
	DefaultHost     = "127.0.0.1"
	DefaultPort     = 11500
	DefaultTimeout  = 600 * time.Second

	// DefaultMaxConcurrent bounds requests that are in flight at once.
	DefaultMaxConcurrent = 8
	// DefaultMaxBodySize bounds bodies read for JSON injection (1 MiB).
	DefaultMaxBodySize int64 = 1 << 20
)

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
	Upstream    string
	UpstreamURL *url.URL
	Timeout     time.Duration
	Params      map[string]json.RawMessage
	Lang        i18n.Lang
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
	mu          sync.RWMutex
	upstream    string
	upstreamURL *url.URL
	timeout     time.Duration
	params      map[string]json.RawMessage
	lang        i18n.Lang
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

// Snapshot returns all mutable settings at once.
func (c *Config) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Upstream:    c.upstream,
		UpstreamURL: c.upstreamURL,
		Timeout:     c.timeout,
		Params:      cloneParams(c.params),
		Lang:        c.lang,
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
	params := c.Params()
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
	return map[string]any{
		"host":     c.Host,
		"port":     c.Port,
		"upstream": snap.Upstream,
		"timeout":  snap.Timeout.Seconds(),
		"params":   snap.Params,
		"lang":     snap.Lang.String(),
	}
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
	document := map[string]any{
		"host":     c.Host,
		"port":     c.Port,
		"upstream": normalized,
		"timeout":  timeout.Seconds(),
		"params":   params,
		"lang":     lang.String(),
	}
	if err := writeConfig(c.ConfigPath, document); err != nil {
		return fmt.Errorf(i18n.T("cfg.errWriteConfig"), err)
	}

	c.mu.Lock()
	c.upstream, c.upstreamURL, c.timeout, c.params, c.lang = normalized, parsed, timeout, params, lang
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

// Parse resolves args (without argv[0]) into a Config.
//
// getenv is injectable so tests do not depend on the ambient environment;
// it must report whether a variable is present (PROXY_PARAMS=” differs from
// an unset PROXY_PARAMS). A nil getenv uses os.LookupEnv.
func Parse(args []string, getenv func(string) (string, bool)) (*Config, error) {
	if getenv == nil {
		getenv = os.LookupEnv
	}

	var (
		host        string
		port        int
		portSet     bool
		upstream    string
		sets        []string
		timeoutSecs float64
		timeoutSet  bool
		configPath  string
		logPath     string
		foreground  bool
		noBrowser   bool
		noDefaults  bool
		maxConc     int
		maxConcSet  bool
		maxBody     int64
		maxBodySet  bool
		verbose     bool
		showVersion bool
		langFlag    string
	)

	// The language has to be settled before the flag parser can complain, so an
	// invalid flag is reported in the language the user asked for. A bad --lang
	// value is reported by the real Parse below, once the flag is known.
	preLang, _ := resolveLang(prescanLang(args), "", getenv)
	i18n.Set(preLang)

	fs := flag.NewFlagSet("ollama-nothink-proxy", flag.ContinueOnError)
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

	configPath = expandPath(configPathOr(configPath))
	logPath = expandPath(logPathOr(logPath))
	stored := loadFile(configPath)

	// CLI flag > ONP_LANG > config file > locale. envValue folds ONP_LANG in
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

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// resolveLang picks the runtime language: an explicit --lang beats ONP_LANG,
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
		if env, ok := getenv("ONP_LANG"); ok && !strings.EqualFold(strings.TrimSpace(env), i18n.Auto) {
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
	host       string
	port       int
	portSet    bool
	upstream   string
	timeout    float64
	timeoutSet bool
	params     map[string]json.RawMessage
	paramsSet  bool
	lang       string
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
		return filepath.Join(home, "Library", "Application Support", "ollama-nothink-proxy", "config.json")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ollama-nothink-proxy", "config.json")
}

// DefaultLogPath is where request logs live when --log is omitted.
func DefaultLogPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Logs", "ollama-nothink-proxy.log")
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ollama-nothink-proxy", "ollama-nothink-proxy.log")
}

// Commands accepted as the first argument. Everything after it is parsed as
// normal flags, so `restart --port 11501` restarts on another port.
const (
	// CommandServe starts the proxy (the default when no command is given).
	CommandServe = "serve"
	// CommandRestart stops the running instance and starts a fresh one.
	CommandRestart = "restart"
)

// SplitCommand pulls an optional leading subcommand off args. Anything it does
// not recognise is left in place so the flag parser reports it.
func SplitCommand(args []string) (command string, rest []string) {
	if len(args) > 0 {
		switch args[0] {
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
		DefaultUpstream,
		strconv.Itoa(int(DefaultTimeout.Seconds())),
		strconv.Itoa(DefaultMaxConcurrent),
		strconv.FormatInt(DefaultMaxBodySize, 10),
		DefaultConfigPath(),
		DefaultLogPath(),
	)
}
