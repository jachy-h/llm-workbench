package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/policy"
)

func envOf(pairs map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok
	}
}

// tempPaths gives every test its own config/log paths so the user's real
// files never leak into the assertions.
func tempPaths(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	return []string{"--config", filepath.Join(dir, "config.json"), "--log", filepath.Join(dir, "proxy.log"), "--db", filepath.Join(dir, "workbench.db")}
}

func parse(t *testing.T, args []string, env map[string]string) *Config {
	t.Helper()
	cfg, err := Parse(args, envOf(env))
	if err != nil {
		t.Fatalf("Parse(%v) error: %v", args, err)
	}
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := parse(t, tempPaths(t), nil)

	if cfg.Host != DefaultHost {
		t.Errorf("Host = %q, want %q", cfg.Host, DefaultHost)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.Upstream() != DefaultUpstream {
		t.Errorf("Upstream = %q, want %q", cfg.Upstream(), DefaultUpstream)
	}
	if cfg.Timeout() != DefaultTimeout {
		t.Errorf("Timeout = %v, want %v", cfg.Timeout(), DefaultTimeout)
	}
	if cfg.MaxConcurrent() != DefaultMaxConcurrent {
		t.Errorf("MaxConcurrent = %d, want %d", cfg.MaxConcurrent(), DefaultMaxConcurrent)
	}
	if cfg.MaxBodySize() != DefaultMaxBodySize {
		t.Errorf("MaxBodySize = %d, want %d", cfg.MaxBodySize(), DefaultMaxBodySize)
	}
	resolved := cfg.Policy()
	if resolved.Thinking != policy.ThinkingOff {
		t.Errorf("Thinking = %q, want off", resolved.Thinking)
	}
	if len(resolved.Params) != 0 {
		t.Errorf("default policy injects params: %v", resolved.Params)
	}
	if !resolved.SystemPrompt.Empty() {
		t.Errorf("default policy injects a system prompt: %+v", resolved.SystemPrompt)
	}
}

func TestParamsAndSystemPromptAreIgnored(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"params": {"temperature": 0.5}, "system_prompt": {"mode": "override", "text": "fixed"}}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	resolved := cfg.Policy()
	if len(resolved.Params) != 0 {
		t.Errorf("legacy params still injected: %v", resolved.Params)
	}
	if !resolved.SystemPrompt.Empty() {
		t.Errorf("legacy system prompt still injected: %+v", resolved.SystemPrompt)
	}
	if _, ok := cfg.Snapshot().Extra["params"]; !ok {
		t.Error("legacy params key was dropped instead of preserved as unknown")
	}
}

func TestFileAndCLIPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"host": "127.0.0.2", "port": 3000, "upstream": "http://file:11434", "timeout": 30}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "p.log")

	cfg := parse(t, []string{"--config", cfgPath, "--log", logPath}, nil)
	if cfg.Host != "127.0.0.2" || cfg.Port != 3000 || cfg.Upstream() != "http://file:11434" {
		t.Errorf("file config not applied: %+v", cfg)
	}
	if cfg.Timeout() != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout())
	}

	cfg = parse(t, []string{"--config", cfgPath, "--log", logPath, "--port", "5000", "--host", "127.0.0.3"}, nil)
	if cfg.Port != 5000 || cfg.Host != "127.0.0.3" {
		t.Errorf("CLI should win over the file: %+v", cfg)
	}
}

func TestUpstreamNormalization(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"upstream": "http://127.0.0.1:11434/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	if cfg.Upstream() != "http://127.0.0.1:11434" {
		t.Errorf("Upstream = %q", cfg.Upstream())
	}
	if got := cfg.UpstreamURL().String(); got != "http://127.0.0.1:11434" {
		t.Errorf("UpstreamURL = %q", got)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"removed --set flag", []string{"--set", "temperature=0"}, nil},
		{"bad port", []string{"--port", "abc"}, nil},
		{"port out of range", []string{"--port", "70000"}, nil},
		{"unknown flag", []string{"--nope"}, nil},
		{"removed flag", []string{"--upstream", "http://127.0.0.1:11434"}, nil},
		{"positional arg", []string{"extra"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(tempPaths(t), tc.args...)
			if _, err := Parse(args, envOf(tc.env)); err == nil {
				t.Fatalf("Parse(%v) succeeded, want error", args)
			}
		})
	}
}

func TestVersionRequested(t *testing.T) {
	for _, arg := range []string{"--version", "-v"} {
		if _, err := Parse([]string{arg}, envOf(nil)); err != ErrVersionRequested {
			t.Fatalf("%s: err = %v, want ErrVersionRequested", arg, err)
		}
	}
}

func TestDefaultPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	appDir := filepath.Join(home, ".llm-workbench")
	wantConfig := filepath.Join(appDir, "config.jsonc")
	wantLog := filepath.Join(appDir, "logs", "llm-workbench.log")
	wantData := filepath.Join(appDir, "workbench.db")
	if got := DefaultConfigPath(); got != wantConfig {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, wantConfig)
	}
	if got := ConfigDir(); got != appDir {
		t.Errorf("ConfigDir() = %q, want %q", got, appDir)
	}
	if got := DefaultLogPath(); got != wantLog {
		t.Errorf("DefaultLogPath() = %q, want %q", got, wantLog)
	}
	if got := DefaultDataPath(); got != wantData {
		t.Errorf("DefaultDataPath() = %q, want %q", got, wantData)
	}

	cfg := parse(t, []string{"--db", filepath.Join(home, "proxy.db")}, nil)
	if cfg.ConfigPath != wantConfig {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, wantConfig)
	}
	if cfg.LogPath != wantLog {
		t.Errorf("LogPath = %q, want %q", cfg.LogPath, wantLog)
	}
	if cfg.Port != 11500 {
		t.Errorf("Port = %d, want 11500", cfg.Port)
	}
	if !filepath.IsAbs(cfg.LogPath) || filepath.Ext(cfg.LogPath) != ".log" {
		t.Errorf("LogPath = %q", cfg.LogPath)
	}
}

func TestDefaultPathsIgnoreXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	appDir := filepath.Join(home, ".llm-workbench")
	if got := DefaultConfigPath(); got != filepath.Join(appDir, "config.jsonc") {
		t.Errorf("DefaultConfigPath() = %q", got)
	}
	if got := DefaultLogPath(); got != filepath.Join(appDir, "logs", "llm-workbench.log") {
		t.Errorf("DefaultLogPath() = %q", got)
	}
	if got := DefaultDataPath(); got != filepath.Join(appDir, "workbench.db") {
		t.Errorf("DefaultDataPath() = %q", got)
	}
}

func TestConfigFileAcceptsJSONC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	body := `{
  // the upstream still looks like a comment to a naive stripper
  "upstream": "http://127.0.0.1:11434", /* keeps the scheme */
  "thinking": "on",
  "params": {
    // low temperature, trailing comma below is fine
    "temperature": 0.1,
  },
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", path, "--log", filepath.Join(dir, "proxy.log")}, nil)

	if got := cfg.Upstream(); got != DefaultUpstream {
		t.Errorf("Upstream = %q, want %q", got, DefaultUpstream)
	}
	if got := cfg.Thinking(); got != policy.ThinkingOn {
		t.Errorf("Thinking = %q, want on", got)
	}
	if len(cfg.Policy().Params) != 0 {
		t.Errorf("params from config file were injected: %v", cfg.Policy().Params)
	}
}

func TestMigratesLegacyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	legacy := filepath.Join(home, "Library", "Application Support", "llm-workbench", "config.json")
	body := `{"port": 11434, "thinking": "on", "future_key": {"a": 1}}`
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--log", filepath.Join(home, "proxy.log")}, nil)
	if cfg.MigratedFrom != legacy {
		t.Errorf("MigratedFrom = %q, want %q", cfg.MigratedFrom, legacy)
	}
	// 11434 is Ollama's port, so the migrated listen port is corrected.
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if got := cfg.Thinking(); got != policy.ThinkingOn {
		t.Errorf("Thinking = %q, want the migrated value", got)
	}

	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatalf("migrated config missing: %v", err)
	}
	for _, needle := range []string{`"port": 11500`, `"thinking": "on"`, `"future_key"`} {
		if !strings.Contains(string(data), needle) {
			t.Errorf("migrated config lacks %q:\n%s", needle, data)
		}
	}
	if _, err := os.Stat(legacy + ConfigBackupSuffix); err != nil {
		t.Errorf("legacy file not kept as backup: %v", err)
	}

	// A second start finds the new file and imports nothing.
	again := parse(t, []string{"--log", filepath.Join(home, "proxy.log"), "--db", filepath.Join(home, "proxy.db")}, nil)
	if again.MigratedFrom != "" {
		t.Errorf("second start migrated again from %q", again.MigratedFrom)
	}
}

func TestMigratesLegacyJSONCConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	legacy := filepath.Join(home, ".config", "llm-workbench", "config.jsonc")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{\n  // old default\n  \"thinking\": \"on\",\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--log", filepath.Join(home, "proxy.log"), "--db", filepath.Join(home, "proxy.db")}, nil)
	if cfg.MigratedFrom != legacy {
		t.Errorf("MigratedFrom = %q, want %q", cfg.MigratedFrom, legacy)
	}
	if cfg.ConfigPath != filepath.Join(home, ".llm-workbench", "config.jsonc") {
		t.Errorf("ConfigPath = %q", cfg.ConfigPath)
	}
	if cfg.Thinking() != policy.ThinkingOn {
		t.Errorf("Thinking = %q, want on", cfg.Thinking())
	}
	if _, err := os.Stat(legacy + ConfigBackupSuffix); err != nil {
		t.Errorf("legacy file not kept as backup: %v", err)
	}
}

func TestLegacyLogsAreRemoved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	old := []string{
		filepath.Join(home, "Library", "Logs", "llm-workbench.log"),
		filepath.Join(home, ".local", "state", "llm-workbench", "llm-workbench.log"),
	}
	for _, path := range old {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old log\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := parse(t, []string{"--db", filepath.Join(home, "proxy.db")}, nil)
	if cfg.LogPath != filepath.Join(home, ".llm-workbench", "logs", "llm-workbench.log") {
		t.Errorf("LogPath = %q", cfg.LogPath)
	}
	// Parsing is side-effect free for logs; main removes them once it knows
	// no older instance is still writing.
	for _, path := range old {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Parse removed legacy log %q too early: %v", path, err)
		}
	}
	RemoveLegacyLogs(cfg.LogPath)
	for _, path := range old {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("legacy log %q still exists (err %v)", path, err)
		}
	}
}

func TestRemoveLegacyLogsKeepsCurrentFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")

	current := DefaultLogPath()
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("new log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RemoveLegacyLogs(current)
	if _, err := os.Stat(current); err != nil {
		t.Errorf("current log was removed: %v", err)
	}
}

func TestMigratesLegacyDatabaseAndRewritesDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	legacy := filepath.Join(home, "Library", "Application Support", "llm-workbench", "workbench.db")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("sqlite bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Older releases wrote their platform default into the config file.
	current := DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatal(err)
	}
	document := `{"storage": {"path": ` + strconv.Quote(legacy) + `}}`
	if err := os.WriteFile(current, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--log", filepath.Join(home, "proxy.log")}, nil)
	want := filepath.Join(home, ".llm-workbench", "workbench.db")
	if cfg.Storage().Path != want {
		t.Errorf("Storage.Path = %q, want %q", cfg.Storage().Path, want)
	}
	// Parsing does not move the file: another instance may still use it.
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("Parse removed the legacy database too early: %v", err)
	}
	if from := MigrateData(want); from != legacy {
		t.Errorf("MigrateData = %q, want %q", from, legacy)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("migrated database missing: %v", err)
	}
	if string(data) != "sqlite bytes" {
		t.Errorf("migrated database = %q", data)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy database still exists (err %v)", err)
	}
	// A second migration leaves the existing database alone.
	if from := MigrateData(want); from != "" {
		t.Errorf("second MigrateData moved %q", from)
	}
}

func TestExistingConfigWinsOverLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	current := DefaultConfigPath()
	legacy := filepath.Join(home, ".config", "llm-workbench", "config.json")
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(`{"lang": "en"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"lang": "zh"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--log", filepath.Join(home, "proxy.log"), "--db", filepath.Join(home, "proxy.db")}, nil)
	if cfg.MigratedFrom != "" {
		t.Errorf("migrated over an existing config from %q", cfg.MigratedFrom)
	}
	if got := cfg.Lang(); got != i18n.EN {
		t.Errorf("Lang = %q, want the existing en config", got)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy file was touched: %v", err)
	}
}

func TestListenPortNeverCollidesWithUpstream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	args := []string{"--config", path, "--log", filepath.Join(dir, "proxy.log")}

	// The stored value is corrected and written back once.
	if err := os.WriteFile(path, []byte(`{"port": 11434}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, args, nil)
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"port": 11500`) {
		t.Errorf("corrected port was not persisted:\n%s", data)
	}

	// Listening on the upstream's own port would forward to itself.
	if err := os.WriteFile(path, []byte(`{"port": 8080, "upstream": "http://127.0.0.1:8080"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := parse(t, args, nil).Port; got != DefaultPort {
		t.Errorf("Port = %d, want %d for an upstream on the same port", got, DefaultPort)
	}

	// A flag is corrected too, but the file is left alone: the flag was the
	// problem, not the stored value.
	flagged := parse(t, append(args, "--port", "11434"), nil)
	if flagged.Port != DefaultPort || flagged.PortRequested != 11434 {
		t.Errorf("--port 11434 = %d (requested %d), want %d", flagged.Port, flagged.PortRequested, DefaultPort)
	}
	// Any other port is untouched.
	if got := parse(t, append(args, "--port", "12000"), nil).Port; got != 12000 {
		t.Errorf("Port = %d, want 12000", got)
	}
}

func TestApplyPersistsAndUpdates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)

	payload := map[string]json.RawMessage{
		"upstream":       json.RawMessage(`"http://127.0.0.1:11435/"`),
		"timeout":        json.RawMessage(`30`),
		"max_concurrent": json.RawMessage(`3`),
		"max_body_size":  json.RawMessage(`2048`),
	}
	if err := cfg.Apply(payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := cfg.Upstream(); got != "http://127.0.0.1:11435" {
		t.Errorf("Upstream = %q", got)
	}
	if got := cfg.Timeout(); got != 30*time.Second {
		t.Errorf("Timeout = %v", got)
	}
	if cfg.MaxConcurrent() != 3 || cfg.MaxBodySize() != 2048 {
		t.Errorf("limits = %d/%d", cfg.MaxConcurrent(), cfg.MaxBodySize())
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("config file JSON: %v", err)
	}
	if document["upstream"] != "http://127.0.0.1:11435" {
		t.Errorf("saved upstream = %v", document["upstream"])
	}
	if document["timeout"] != float64(30) {
		t.Errorf("saved timeout = %v", document["timeout"])
	}
	if document["host"] != DefaultHost || document["port"] != float64(DefaultPort) {
		t.Errorf("saved host/port = %v/%v", document["host"], document["port"])
	}
	if document["max_concurrent"] != float64(3) || document["max_body_size"] != float64(2048) {
		t.Errorf("saved limits = %v/%v", document["max_concurrent"], document["max_body_size"])
	}
}

func TestApplyRejectsBadPayloads(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)

	cases := map[string]string{
		"ftp upstream":     `{"upstream":"ftp://x"}`,
		"blank upstream":   `{"upstream":"   "}`,
		"numeric upstream": `{"upstream":123}`,
		"zero timeout":     `{"timeout":0}`,
		"bad timeout":      `{"timeout":"abc"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Apply(payload); err == nil {
				t.Fatalf("Apply(%s) succeeded, want error", raw)
			}
			if cfg.Upstream() != DefaultUpstream || cfg.Timeout() != DefaultTimeout {
				t.Errorf("rejected Apply mutated config: %+v", cfg.Snapshot())
			}
		})
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("rejected Apply wrote the config file (err=%v)", err)
	}
}

func TestSaveWritesCurrentDocument(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nested", "config.json")
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	if err := cfg.Apply(map[string]json.RawMessage{"thinking": json.RawMessage(`"on"`)}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document["thinking"] != "on" {
		t.Errorf("thinking = %v", document["thinking"])
	}
	if _, ok := document["params"]; ok {
		t.Errorf("params written to a new config: %v", document["params"])
	}
}

func TestListenURLWildcard(t *testing.T) {
	cfg := parse(t, append(tempPaths(t), "--host", "0.0.0.0", "--port", "12345"), nil)
	if got := cfg.ListenURL(); got != "http://127.0.0.1:12345" {
		t.Errorf("ListenURL = %q", got)
	}
	if got := cfg.ListenAddr(); got != "0.0.0.0:12345" {
		t.Errorf("ListenAddr = %q", got)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		args    []string
		command string
		rest    []string
	}{
		{nil, CommandServe, nil},
		{[]string{"--port", "11500"}, CommandServe, []string{"--port", "11500"}},
		{[]string{"restart"}, CommandRestart, []string{}},
		{[]string{"restart", "--port", "11501"}, CommandRestart, []string{"--port", "11501"}},
		{[]string{"stop"}, CommandStop, []string{}},
		{[]string{"stop", "--port", "11501"}, CommandStop, []string{"--port", "11501"}},
		{[]string{"start", "--port", "11502"}, CommandServe, []string{"--port", "11502"}},
		{[]string{"serve", "--port", "11502"}, CommandServe, []string{"--port", "11502"}},
		// Unknown positionals stay put so flag parsing reports them.
		{[]string{"bogus"}, CommandServe, []string{"bogus"}},
	}
	for _, tc := range cases {
		command, rest := SplitCommand(tc.args)
		if command != tc.command {
			t.Errorf("SplitCommand(%v) command = %q, want %q", tc.args, command, tc.command)
		}
		if len(rest) != len(tc.rest) {
			t.Errorf("SplitCommand(%v) rest = %v, want %v", tc.args, rest, tc.rest)
			continue
		}
		for i := range rest {
			if rest[i] != tc.rest[i] {
				t.Errorf("SplitCommand(%v) rest = %v, want %v", tc.args, rest, tc.rest)
				break
			}
		}
	}
}

func TestUsageMentionsCommands(t *testing.T) {
	text := UsageText()
	for _, needle := range []string{"restart", "stop", "start"} {
		if !strings.Contains(text, needle) {
			t.Errorf("usage does not mention %q:\n%s", needle, text)
		}
	}
}

func TestConfigFileThinkingAndLegacySystemPrompt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"thinking":"passthrough","system_prompt":{"mode":"override","text":"fixed"}}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	resolved := cfg.Policy()
	if resolved.Thinking != policy.ThinkingPassthrough {
		t.Errorf("thinking = %q", resolved.Thinking)
	}
	if !resolved.SystemPrompt.Empty() {
		t.Errorf("legacy system prompt still applied: %+v", resolved.SystemPrompt)
	}
}

func TestRemovedProfileSettingsAreRejected(t *testing.T) {
	dir := t.TempDir()
	for name, file := range map[string]string{
		"profiles": `{"profiles":{"fast":{"thinking":"off"}}}`,
		"profile":  `{"profile":"fast"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Parse([]string{"--config", path, "--log", filepath.Join(dir, "p.log"),
				"--db", filepath.Join(dir, "workbench.db")}, envOf(nil))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Parse error = %v, want the removed key %q", err, name)
			}
		})
	}

	cfg := parse(t, tempPaths(t), nil)
	if err := cfg.Apply(map[string]json.RawMessage{"profiles": json.RawMessage(`{}`)}); err == nil {
		t.Error("Apply accepted profiles")
	}
	if err := cfg.Apply(map[string]json.RawMessage{"profile": json.RawMessage(`""`)}); err == nil {
		t.Error("Apply accepted profile")
	}
}

func TestInvalidPolicyConfig(t *testing.T) {
	dir := t.TempDir()
	for name, file := range map[string]string{
		"bad thinking": `{"thinking":"maybe"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
				t.Fatal(err)
			}
			// A malformed optional section is ignored on load; it must not crash.
			cfg, err := Parse([]string{"--config", path, "--log", filepath.Join(dir, "p.log"), "--db", filepath.Join(dir, "workbench.db")}, envOf(nil))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg == nil {
				t.Fatal("nil config")
			}
		})
	}
}

func TestApplyPolicyAndPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"inspector":{"capture_bodies":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)

	payload := map[string]json.RawMessage{
		"thinking": json.RawMessage(`"on"`),
	}
	if err := cfg.Apply(payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	resolved := cfg.Policy()
	if resolved.Thinking != policy.ThinkingOn {
		t.Errorf("policy = %+v", resolved)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["inspector"]; !ok {
		t.Errorf("unknown key was dropped: %v", document)
	}
	if document["thinking"] != "on" {
		t.Errorf("document = %v", document)
	}
	if _, ok := document["profiles"]; ok {
		t.Errorf("removed key was written back: %v", document)
	}

	// Rejected updates must not change the live policy.
	if err := cfg.Apply(map[string]json.RawMessage{"thinking": json.RawMessage(`"bad"`)}); err == nil {
		t.Fatal("bad thinking was accepted")
	}
	if got := cfg.Thinking(); got != policy.ThinkingOn {
		t.Errorf("global thinking after rejected Apply = %q", got)
	}
}

func TestInspectorAndStorageConfig(t *testing.T) {
	cfg := parse(t, tempPaths(t), nil)
	inspectorState := cfg.Inspector()
	if !inspectorState.Enabled || inspectorState.CaptureBodies {
		t.Errorf("defaults = %+v", inspectorState)
	}
	if inspectorState.MaxBodyBytes != DefaultInspectorMaxBodyBytes || inspectorState.RetentionDays != DefaultRetentionDays {
		t.Errorf("defaults = %+v", inspectorState)
	}
	if cfg.Storage().Path == "" {
		t.Errorf("storage path = %q", cfg.Storage().Path)
	}

	dbPath := filepath.Join(t.TempDir(), "custom.db")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"inspector":{"enabled":false,"capture_bodies":true,"retention_days":3,"max_rows":12}}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"), "--db", dbPath}, nil)
	inspectorState = cfg.Inspector()
	if inspectorState.Enabled || !inspectorState.CaptureBodies {
		t.Errorf("file = %+v", inspectorState)
	}
	if inspectorState.RetentionDays != 3 || inspectorState.MaxRows != 12 {
		t.Errorf("retention = %+v", inspectorState)
	}
	if cfg.Storage().Path != dbPath {
		t.Errorf("storage path = %q, want %q", cfg.Storage().Path, dbPath)
	}
}

func TestApplyInspectorAndStorage(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)

	payload := map[string]json.RawMessage{
		"inspector": json.RawMessage(`{"capture_bodies":true,"max_body_bytes":1024,"retention_days":2,"max_rows":7}`),
		"storage":   json.RawMessage(`{"path":"` + filepath.ToSlash(filepath.Join(dir, "db.sqlite")) + `"}`),
	}
	if err := cfg.Apply(payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	inspectorState := cfg.Inspector()
	if !inspectorState.CaptureBodies || inspectorState.MaxBodyBytes != 1024 || inspectorState.RetentionDays != 2 || inspectorState.MaxRows != 7 {
		t.Errorf("inspector = %+v", inspectorState)
	}
	if cfg.Storage().Path != filepath.Join(dir, "db.sqlite") {
		t.Errorf("storage = %q", cfg.Storage().Path)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["inspector"]; !ok {
		t.Errorf("document missing inspector: %v", document)
	}
	if _, ok := document["storage"]; !ok {
		t.Errorf("document missing storage: %v", document)
	}

	for _, raw := range []string{
		`{"inspector":{"capture_bodies":"yes"}}`,
		`{"inspector":{"retention_days":-1}}`,
		`{"storage":{"path":42}}`,
	} {
		var bad map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &bad); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Apply(bad); err == nil {
			t.Errorf("Apply(%s) succeeded", raw)
		}
	}
}

func TestLanguageResolution(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })

	if got := parse(t, tempPaths(t), nil).Lang(); got != i18n.ZH {
		t.Errorf("default Lang = %q, want zh", got)
	}
	if got := parse(t, tempPaths(t), map[string]string{"LANG": "en_US.UTF-8"}).Lang(); got != i18n.EN {
		t.Errorf("LANG=en_US.UTF-8 Lang = %q", got)
	}
	// A language variable is no longer an input: only the locale counts.
	if got := parse(t, tempPaths(t), map[string]string{
		"LLM_WORKBENCH_LANG": "en", "LANG": "zh_CN.UTF-8",
	}).Lang(); got != i18n.ZH {
		t.Errorf("LLM_WORKBENCH_LANG should be ignored, Lang = %q", got)
	}

	// The config file wins over the locale.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"lang":"en"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path, "--log", filepath.Join(dir, "proxy.log")}
	if got := parse(t, args, map[string]string{"LANG": "zh_CN.UTF-8"}).Lang(); got != i18n.EN {
		t.Errorf("file lang = %q, want en", got)
	}
	// The stored value must be a real language; anything else falls through.
	if got := parse(t, args, nil).Lang(); got != i18n.EN {
		t.Errorf("file lang = %q, want en", got)
	}
}

func TestApplyPersistsLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	cfg := parse(t, tempPaths(t), nil)

	if err := cfg.Apply(map[string]json.RawMessage{"lang": json.RawMessage(`"en"`)}); err != nil {
		t.Fatalf("Apply(lang) error: %v", err)
	}
	if got := cfg.Lang(); got != i18n.EN {
		t.Errorf("Lang after Apply = %q, want en", got)
	}
	if got := i18n.Current(); got != i18n.EN {
		t.Errorf("runtime language after Apply = %q, want en", got)
	}
	raw, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"lang": "en"`) {
		t.Errorf("config file has no lang:\n%s", raw)
	}

	for _, payload := range []string{`"fr"`, `42`, `null`} {
		if err := cfg.Apply(map[string]json.RawMessage{"lang": json.RawMessage(payload)}); err == nil {
			t.Errorf("Apply(lang=%s) was accepted", payload)
		}
	}
	// A rejected value changes nothing.
	if got := cfg.Lang(); got != i18n.EN {
		t.Errorf("Lang after rejected Apply = %q, want en", got)
	}
}
