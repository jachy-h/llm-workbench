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
	if cfg.MaxConcurrent != DefaultMaxConcurrent {
		t.Errorf("MaxConcurrent = %d, want %d", cfg.MaxConcurrent, DefaultMaxConcurrent)
	}
	if cfg.MaxBodySize != DefaultMaxBodySize {
		t.Errorf("MaxBodySize = %d, want %d", cfg.MaxBodySize, DefaultMaxBodySize)
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

func TestProxyParamsReplacesDefaults(t *testing.T) {
	cfg := parse(t, tempPaths(t), map[string]string{
		"PROXY_PARAMS": `{"temperature": 0.5, "max_tokens": 12}`,
	})
	if len(cfg.Params()) != 2 {
		t.Fatalf("Params = %v, want exactly the env object", cfg.Params())
	}
	if string(cfg.Params()["temperature"]) != "0.5" {
		t.Errorf("temperature = %s", cfg.Params()["temperature"])
	}
}

func TestProxyParamsEmptyMeansInjectNothing(t *testing.T) {
	for _, value := range []string{"", "{}"} {
		cfg := parse(t, tempPaths(t), map[string]string{"PROXY_PARAMS": value})
		if len(cfg.Params()) != 0 {
			t.Errorf("PROXY_PARAMS=%q produced %v, want empty", value, cfg.Params())
		}
	}
}

func TestParamPriorityFileEnvSet(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"params": {"max_tokens": 12, "top_p": 0.1}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log")},
		map[string]string{"PROXY_PARAMS": `{"temperature": 0.5}`})
	if len(cfg.Params()) != 1 || string(cfg.Params()["temperature"]) != "0.5" {
		t.Fatalf("PROXY_PARAMS should replace file params, got %v", cfg.Params())
	}

	cfg = parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"), "--set", "max_tokens=7"}, nil)
	if string(cfg.Params()["max_tokens"]) != "7" {
		t.Errorf("--set should win over file params, got %s", cfg.Params()["max_tokens"])
	}
	if string(cfg.Params()["top_p"]) != "0.1" {
		t.Errorf("other file params should survive, got %v", cfg.Params())
	}
}

func TestNoDefaults(t *testing.T) {
	cfg := parse(t, append(tempPaths(t), "--no-defaults", "--set", "temperature=0"), nil)
	if len(cfg.Params()) != 1 || string(cfg.Params()["temperature"]) != "0" {
		t.Fatalf("Params = %v, want only temperature", cfg.Params())
	}
}

func TestEnvAndCLIPrecedence(t *testing.T) {
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

	cfg = parse(t, []string{"--config", cfgPath, "--log", logPath}, map[string]string{
		"PORT": "4000", "UPSTREAM": "http://env:11434",
	})
	if cfg.Port != 4000 || cfg.Upstream() != "http://env:11434" {
		t.Errorf("env should win over file: %+v", cfg)
	}

	cfg = parse(t, []string{"--config", cfgPath, "--log", logPath, "--port", "5000", "--upstream", "http://cli:11434"}, map[string]string{
		"PORT": "4000", "UPSTREAM": "http://env:11434",
	})
	if cfg.Port != 5000 || cfg.Upstream() != "http://cli:11434" {
		t.Errorf("CLI should win over env: %+v", cfg)
	}
}

func TestInvalidEnvPortFallsBack(t *testing.T) {
	cfg := parse(t, tempPaths(t), map[string]string{"PORT": "not-a-port"})
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want default %d", cfg.Port, DefaultPort)
	}
}

func TestUpstreamNormalization(t *testing.T) {
	cfg := parse(t, append(tempPaths(t), "--upstream", "http://127.0.0.1:11434/"), nil)
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
		{"empty set key ok but bad value? none", []string{"--config", "/nonexistent", "--set", "=", "--upstream", "ftp://x"}, nil},
		{"bad proxy params", nil, map[string]string{"PROXY_PARAMS": `[1,2]`}},
		{"null proxy params", nil, map[string]string{"PROXY_PARAMS": `null`}},
		{"ftp upstream", []string{"--upstream", "ftp://x"}, nil},
		{"empty upstream", []string{"--upstream", "  "}, nil},
		{"zero timeout", []string{"--timeout", "0"}, nil},
		{"zero concurrency", []string{"--max-concurrent", "0"}, nil},
		{"zero body size", []string{"--max-body-size", "0"}, nil},
		{"bad port", []string{"--port", "abc"}, nil},
		{"unknown flag", []string{"--nope"}, nil},
		{"positional arg", []string{"extra"}, nil},
		{"boolean flag value", []string{"--verbose=maybe"}, nil},
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
	if _, err := Parse([]string{"--version"}, envOf(nil)); err != ErrVersionRequested {
		t.Fatalf("err = %v, want ErrVersionRequested", err)
	}
}

func TestDefaultPaths(t *testing.T) {
	cfg := parse(t, nil, nil)
	if !filepath.IsAbs(cfg.ConfigPath) || filepath.Base(cfg.ConfigPath) != "config.json" {
		t.Errorf("ConfigPath = %q", cfg.ConfigPath)
	}
	if !filepath.IsAbs(cfg.LogPath) || filepath.Ext(cfg.LogPath) != ".log" {
		t.Errorf("LogPath = %q", cfg.LogPath)
	}
	if strings.Contains(cfg.ConfigPath, LegacyDir) {
		t.Errorf("DefaultConfigPath still uses the legacy directory: %q", cfg.ConfigPath)
	}
}

func TestImportLegacyConfig(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))

	legacy := LegacyConfigPath()
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"upstream":"http://legacy:11434","params":{"temperature":0.7},"lang":"en"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Runtime parsing reads the old file when the new path is missing, but
	// keeps writing to the new path.
	cfg, err := ParseRuntime([]string{"--log", filepath.Join(dir, "x.log")}, envOf(nil))
	if err != nil {
		t.Fatalf("ParseRuntime: %v", err)
	}
	if cfg.Upstream() != "http://legacy:11434" {
		t.Errorf("legacy upstream = %q", cfg.Upstream())
	}
	if cfg.Lang() != i18n.EN {
		t.Errorf("legacy lang = %q, want en", cfg.Lang())
	}
	if cfg.ConfigPath != DefaultConfigPath() {
		t.Errorf("ConfigPath = %q, want new default %q", cfg.ConfigPath, DefaultConfigPath())
	}
	if _, err := os.Stat(DefaultConfigPath()); !os.IsNotExist(err) {
		t.Errorf("runtime fallback created the new config: %v", err)
	}

	dst := DefaultConfigPath()
	result, err := ImportLegacyConfig(dst)
	if err != nil {
		t.Fatalf("ImportLegacyConfig: %v", err)
	}
	if !result.Copied || result.Reason != "copied" {
		t.Errorf("result = %+v, want copied", result)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy config was removed: %v", err)
	}

	// A second run must not overwrite the new file.
	if err := os.WriteFile(dst, []byte(`{"params":{"temperature":0.1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = ImportLegacyConfig(dst)
	if err != nil {
		t.Fatalf("ImportLegacyConfig second run: %v", err)
	}
	if result.Copied || result.Reason != "destination_exists" {
		t.Errorf("second result = %+v, want destination_exists", result)
	}
	data, _ := os.ReadFile(dst)
	if !strings.Contains(string(data), "0.1") {
		t.Errorf("destination overwritten: %s", data)
	}
}

func TestImportLegacyConfigMissingAndBroken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	dst := filepath.Join(dir, "new", "config.json")

	result, err := ImportLegacyConfig(dst)
	if err != nil {
		t.Fatalf("missing source: %v", err)
	}
	if result.Copied || result.Reason != "source_missing" {
		t.Errorf("result = %+v, want source_missing", result)
	}

	legacy := LegacyConfigPath()
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacyConfig(dst); err == nil {
		t.Error("broken legacy config was copied without an error")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("broken source created the destination: %v", err)
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"missing": {nil, ""},
		"space":   {[]string{"--config", "a.json"}, "a.json"},
		"equals":  {[]string{"--config=a.json"}, "a.json"},
		"other":   {[]string{"--port", "1"}, ""},
	}
	for name, tc := range cases {
		if got := ConfigPathFromArgs(tc.args); got != tc.want {
			t.Errorf("%s: ConfigPathFromArgs(%v) = %q, want %q", name, tc.args, got, tc.want)
		}
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
		"upstream": json.RawMessage(`"http://127.0.0.1:11435/"`),
		"timeout":  json.RawMessage(`30`),
		"params":   json.RawMessage(`{"temperature":0}`),
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
		{[]string{"migrate"}, CommandMigrate, []string{}},
		{[]string{"migrate", "--config", "x.json"}, CommandMigrate, []string{"--config", "x.json"}},
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

func TestUsageMentionsRestart(t *testing.T) {
	if text := UsageText(); !strings.Contains(text, "restart") || !strings.Contains(text, "migrate") {
		t.Errorf("usage does not mention restart/migrate:\n%s", text)
	}
}

func TestThinkingAndSystemPromptFlags(t *testing.T) {
	cfg := parse(t, append(tempPaths(t), "--thinking", "on", "--system-prompt", "be terse"), nil)
	resolved, ok := cfg.Policy("")
	if !ok {
		t.Fatal("Policy failed")
	}
	if resolved.Thinking != policy.ThinkingOn {
		t.Errorf("thinking = %q", resolved.Thinking)
	}
	if resolved.SystemPrompt.Mode != policy.SystemPromptInjectIfMissing || resolved.SystemPrompt.Text != "be terse" {
		t.Errorf("system prompt = %+v", resolved.SystemPrompt)
	}

	override := parse(t, append(tempPaths(t), "--system-prompt", "x", "--system-prompt-override"), nil)
	if got := override.SystemPrompt(); got.Mode != policy.SystemPromptOverride {
		t.Errorf("override mode = %q", got.Mode)
	}
}

func TestSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(path, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, append(tempPaths(t), "--system-prompt-file", path), nil)
	if got := cfg.SystemPrompt(); got.Mode != policy.SystemPromptInjectIfMissing || got.Text != "from file" {
		t.Errorf("system prompt = %+v", got)
	}
	if _, err := Parse(append(tempPaths(t), "--system-prompt", "x", "--system-prompt-file", path), envOf(nil)); err == nil {
		t.Error("both system prompt flags were accepted")
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
	file := `{"profiles":{"fast":{"thinking":"off","params":{"temperature":0.1},"system_prompt":{"mode":"inject-if-missing","text":"fast"}}}}`
	if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"), "--profile", "fast"}, nil)
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
		"--profile", "fast", "--set", "temperature=0.9"}, nil)
	selected, _ := withSet.Policy("")
	if string(selected.Params["temperature"]) != "0.9" {
		t.Errorf("--set did not override profile: %v", selected.Params)
	}

	// Explicit thinking also beats the profile.
	withThinking := parse(t, []string{"--config", cfgPath, "--log", filepath.Join(dir, "p.log"),
		"--profile", "raw", "--thinking", "off"}, nil)
	selected, _ = withThinking.Policy("")
	if selected.Thinking != policy.ThinkingOff {
		t.Errorf("--thinking did not override profile: %q", selected.Thinking)
	}
}

func TestInvalidPolicyConfig(t *testing.T) {
	cases := [][]string{
		{"--thinking", "maybe"},
		{"--profile", "missing"},
	}
	for _, extra := range cases {
		if _, err := Parse(append(tempPaths(t), extra...), envOf(nil)); err == nil {
			t.Errorf("Parse(%v) succeeded", extra)
		}
	}
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
	if cfg.Storage().Path == "" || strings.Contains(cfg.Storage().Path, LegacyDir) {
		t.Errorf("storage path = %q", cfg.Storage().Path)
	}

	dbPath := filepath.Join(t.TempDir(), "custom.db")
	cfg = parse(t, append(tempPaths(t),
		"--no-inspector",
		"--capture-bodies",
		"--db", dbPath,
		"--retention-days", "3",
		"--max-rows", "12",
	), nil)
	inspectorState = cfg.Inspector()
	if inspectorState.Enabled || !inspectorState.CaptureBodies {
		t.Errorf("flags = %+v", inspectorState)
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
	if got := parse(t, tempPaths(t), map[string]string{"LLM_WORKBENCH_LANG": "en"}).Lang(); got != i18n.EN {
		t.Errorf("LLM_WORKBENCH_LANG=en Lang = %q", got)
	}
	// The pre-rename variable remains a lower-priority alias.
	if got := parse(t, tempPaths(t), map[string]string{"ONP_LANG": "en"}).Lang(); got != i18n.EN {
		t.Errorf("ONP_LANG=en Lang = %q", got)
	}
	if got := parse(t, tempPaths(t), map[string]string{"LLM_WORKBENCH_LANG": "zh", "ONP_LANG": "en"}).Lang(); got != i18n.ZH {
		t.Errorf("LLM_WORKBENCH_LANG should beat ONP_LANG, got %q", got)
	}
	if got := parse(t, tempPaths(t), map[string]string{"LANG": "en_US.UTF-8"}).Lang(); got != i18n.EN {
		t.Errorf("LANG=en_US.UTF-8 Lang = %q", got)
	}
	// CLI beats the environment.
	if got := parse(t, append(tempPaths(t), "--lang", "zh"),
		map[string]string{"LLM_WORKBENCH_LANG": "en"}).Lang(); got != i18n.ZH {
		t.Errorf("--lang zh over LLM_WORKBENCH_LANG=en = %q", got)
	}
	// auto defers to the locale.
	if got := parse(t, append(tempPaths(t), "--lang", "auto"),
		map[string]string{"LANG": "en_US.UTF-8"}).Lang(); got != i18n.EN {
		t.Errorf("--lang auto Lang = %q", got)
	}

	// The config file is used only when no flag or variable decides.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"lang":"en"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path, "--log", filepath.Join(dir, "proxy.log")}
	if got := parse(t, args, nil).Lang(); got != i18n.EN {
		t.Errorf("file lang = %q, want en", got)
	}

	// An invalid flag value is an error, and the process keeps working text.
	if _, err := Parse(append(tempPaths(t), "--lang", "fr"), envOf(nil)); err == nil {
		t.Error("--lang fr was accepted")
	}
	// The internal value must be a real language: ZH/EN only.
	if got := parse(t, tempPaths(t), map[string]string{"LLM_WORKBENCH_LANG": "klingon"}).Lang(); got != i18n.ZH {
		t.Errorf("unknown LLM_WORKBENCH_LANG Lang = %q, want zh", got)
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
