package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	return []string{"--config", filepath.Join(dir, "config.json"), "--log", filepath.Join(dir, "proxy.log")}
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
	want := map[string]string{
		"temperature": "0.2",
		"top_p":       "0.9",
		"max_tokens":  "96",
		"keep_alive":  `"30m"`,
	}
	if len(cfg.Params()) != len(want) {
		t.Fatalf("Params = %v, want %v", cfg.Params(), want)
	}
	for k, v := range want {
		if got := string(cfg.Params()[k]); got != v {
			t.Errorf("Params[%q] = %s, want %s", k, got, v)
		}
	}
}

func TestSetCoercion(t *testing.T) {
	cfg := parse(t, append(tempPaths(t),
		"--set", "temperature=0",
		"--set", "top_p=0.5",
		"--set", "keep_alive=1h",
		"--set", "seed=42",
		"--set", "flag=true",
		"--set", "name=abc",
		`--set`, `obj={"a":1}`,
	), nil)

	cases := map[string]string{
		"temperature": "0",
		"top_p":       "0.5",
		"keep_alive":  `"1h"`,
		"seed":        "42",
		"flag":        "true",
		"name":        `"abc"`,
		"obj":         `{"a":1}`,
	}
	for key, want := range cases {
		if got := string(cfg.Params()[key]); got != want {
			t.Errorf("Params[%q] = %s, want %s", key, got, want)
		}
	}
}

func TestParamsFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"params": {"temperature": 0.5, "max_tokens": 12}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	if len(cfg.Params()) != 2 {
		t.Fatalf("Params = %v, want exactly the file object", cfg.Params())
	}
	if string(cfg.Params()["temperature"]) != "0.5" {
		t.Errorf("temperature = %s", cfg.Params()["temperature"])
	}
}

func TestEmptyParamsFileInjectsNothing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"params": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	if len(cfg.Params()) != 0 {
		t.Errorf("empty params file produced %v, want empty", cfg.Params())
	}
}

func TestParamPriorityFileSet(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"params": {"max_tokens": 12, "top_p": 0.1}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"), "--set", "max_tokens=7"}, nil)
	if string(cfg.Params()["max_tokens"]) != "7" {
		t.Errorf("--set should win over file params, got %s", cfg.Params()["max_tokens"])
	}
	if string(cfg.Params()["top_p"]) != "0.1" {
		t.Errorf("other file params should survive, got %v", cfg.Params())
	}
}

func TestFileAndCLIPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"host": "127.0.0.2", "port": 3000, "upstream": "http://file:11434", "timeout": 30, "params": {"seed": 1}}`
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
	if string(cfg.Params()["seed"]) != "1" || len(cfg.Params()) != 1 {
		t.Errorf("Params = %v, want file params only", cfg.Params())
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
		{"set without equals", []string{"--set", "temperature"}, nil},
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

	wantConfig := filepath.Join(home, ".config", "llm-workbench", "config.jsonc")
	if got := DefaultConfigPath(); got != wantConfig {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, wantConfig)
	}
	if got := ConfigDir(); got != filepath.Dir(wantConfig) {
		t.Errorf("ConfigDir() = %q", got)
	}

	cfg := parse(t, []string{"--log", filepath.Join(home, "proxy.log")}, nil)
	if cfg.ConfigPath != wantConfig {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, wantConfig)
	}
	if cfg.Port != 11500 {
		t.Errorf("Port = %d, want 11500", cfg.Port)
	}
	if !filepath.IsAbs(cfg.LogPath) || filepath.Ext(cfg.LogPath) != ".log" {
		t.Errorf("LogPath = %q", cfg.LogPath)
	}
}

func TestDefaultConfigPathHonoursXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "llm-workbench", "config.jsonc")
	if got := DefaultConfigPath(); got != want {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, want)
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
  "profile": "raw", // trailing comma after this object is fine too
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
	if got := string(cfg.Params()["temperature"]); got != "0.1" {
		t.Errorf("temperature = %q", got)
	}
	if got := cfg.Snapshot().Profile; got != "raw" {
		t.Errorf("Profile = %q, want raw", got)
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
	again := parse(t, []string{"--log", filepath.Join(home, "proxy.log")}, nil)
	if again.MigratedFrom != "" {
		t.Errorf("second start migrated again from %q", again.MigratedFrom)
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
	if err := os.WriteFile(legacy, []byte(`{"lang": "zh"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--log", filepath.Join(home, "proxy.log")}, nil)
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

func TestCoerce(t *testing.T) {
	cases := map[string]string{
		"0.5":      "0.5",
		"42":       "42",
		"true":     "true",
		"null":     "null",
		"30m":      `"30m"`,
		"1h":       `"1h"`,
		`"quoted"`: `"quoted"`,
		" 7 ":      "7",
	}
	for in, want := range cases {
		if got := string(Coerce(in)); got != want {
			t.Errorf("Coerce(%q) = %s, want %s", in, got, want)
		}
	}
	if !json.Valid(Coerce("a<b")) {
		t.Error("Coerce produced invalid JSON for a string with markup")
	}
}

func TestApplyPersistsAndUpdates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)

	payload := map[string]json.RawMessage{
		"upstream":       json.RawMessage(`"http://127.0.0.1:11435/"`),
		"timeout":        json.RawMessage(`30`),
		"params":         json.RawMessage(`{"temperature":0}`),
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
	if params := cfg.Params(); len(params) != 1 || string(params["temperature"]) != "0" {
		t.Errorf("Params = %v", params)
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
		"array params":     `{"params":[1,2]}`,
		"null params":      `{"params":null}`,
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
			if len(cfg.Params()) != len(DefaultParams()) {
				t.Errorf("rejected Apply mutated params: %v", cfg.Params())
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
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"),
		"--set", "temperature=0.4"}, nil)
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
	params, ok := document["params"].(map[string]any)
	if !ok || params["temperature"] != 0.4 {
		t.Errorf("params = %v", document["params"])
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

func TestConfigFileThinkingAndSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"thinking":"passthrough","system_prompt":{"mode":"override","text":"fixed"}}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	resolved, _ := cfg.Policy("")
	if resolved.Thinking != policy.ThinkingPassthrough {
		t.Errorf("thinking = %q", resolved.Thinking)
	}
	if resolved.SystemPrompt.Mode != policy.SystemPromptOverride || resolved.SystemPrompt.Text != "fixed" {
		t.Errorf("system prompt = %+v", resolved.SystemPrompt)
	}
}

func TestProfiles(t *testing.T) {
	defaults := DefaultProfiles()
	for _, name := range []string{"nothink", "raw", "thinking"} {
		if _, ok := defaults[name]; !ok {
			t.Errorf("built-in profile %q missing", name)
		}
	}
	if config := parse(t, tempPaths(t), nil); config.ActiveProfile() != "" {
		t.Errorf("active profile = %q, want empty", config.ActiveProfile())
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	file := `{"profile":"fast","profiles":{"fast":{"thinking":"off","params":{"temperature":0.1},"system_prompt":{"mode":"inject-if-missing","text":"fast"}}}}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")}, nil)
	if cfg.ActiveProfile() != "fast" {
		t.Errorf("active profile = %q", cfg.ActiveProfile())
	}
	resolved, ok := cfg.Policy("")
	if !ok || resolved.Thinking != policy.ThinkingOff || resolved.SystemPrompt.Text != "fast" {
		t.Errorf("policy = %+v (ok %v)", resolved, ok)
	}
	if string(resolved.Params["temperature"]) != "0.1" {
		t.Errorf("params = %v", resolved.Params)
	}

	raw, ok := cfg.Policy("raw")
	if !ok || raw.Thinking != policy.ThinkingPassthrough || len(raw.Params) != 0 {
		t.Errorf("raw profile = %+v (ok %v)", raw, ok)
	}
	if _, ok := cfg.Policy("nope"); ok {
		t.Error("unknown profile resolved")
	}

	// --set still wins over the selected profile.
	withSet := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"),
		"--set", "temperature=0.9"}, nil)
	selected, _ := withSet.Policy("")
	if string(selected.Params["temperature"]) != "0.9" {
		t.Errorf("--set did not override profile: %v", selected.Params)
	}
}

func TestInvalidPolicyConfig(t *testing.T) {
	dir := t.TempDir()
	for name, file := range map[string]string{
		"bad thinking":        `{"thinking":"maybe"}`,
		"bad system mode":     `{"system_prompt":{"mode":"sometimes"}}`,
		"profiles not object": `{"profiles":[]}`,
		"profile bad params":  `{"profiles":{"x":{"params":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
				t.Fatal(err)
			}
			// A malformed optional section is ignored on load; it must not crash.
			cfg, err := Parse([]string{"--config", path, "--log", filepath.Join(dir, "p.log")}, envOf(nil))
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
		"thinking":      json.RawMessage(`"on"`),
		"system_prompt": json.RawMessage(`{"mode":"inject-if-missing","text":"hi"}`),
		"profiles":      json.RawMessage(`{"custom":{"thinking":"off","params":{"top_k":5}}}`),
		"profile":       json.RawMessage(`"custom"`),
	}
	if err := cfg.Apply(payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	resolved, ok := cfg.Policy("")
	if !ok || resolved.Thinking != policy.ThinkingOff || string(resolved.Params["top_k"]) != "5" {
		t.Errorf("policy = %+v (ok %v)", resolved, ok)
	}
	if cfg.ActiveProfile() != "custom" {
		t.Errorf("active profile = %q", cfg.ActiveProfile())
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
	if document["thinking"] != "on" || document["profile"] != "custom" {
		t.Errorf("document = %v", document)
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
