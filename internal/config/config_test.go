package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/ollama-nothink-proxy/internal/i18n"
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
	if text := UsageText(); !strings.Contains(text, "restart") {
		t.Errorf("usage does not mention restart:\n%s", text)
	}
}

func TestLanguageResolution(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })

	if got := parse(t, tempPaths(t), nil).Lang(); got != i18n.ZH {
		t.Errorf("default Lang = %q, want zh", got)
	}
	if got := parse(t, tempPaths(t), map[string]string{"ONP_LANG": "en"}).Lang(); got != i18n.EN {
		t.Errorf("ONP_LANG=en Lang = %q", got)
	}
	if got := parse(t, tempPaths(t), map[string]string{"LANG": "en_US.UTF-8"}).Lang(); got != i18n.EN {
		t.Errorf("LANG=en_US.UTF-8 Lang = %q", got)
	}
	// CLI beats the environment.
	if got := parse(t, append(tempPaths(t), "--lang", "zh"),
		map[string]string{"ONP_LANG": "en"}).Lang(); got != i18n.ZH {
		t.Errorf("--lang zh over ONP_LANG=en = %q", got)
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
	if got := parse(t, tempPaths(t), map[string]string{"ONP_LANG": "klingon"}).Lang(); got != i18n.ZH {
		t.Errorf("unknown ONP_LANG Lang = %q, want zh", got)
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
