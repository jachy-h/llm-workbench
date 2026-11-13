// Package tests exercises the compiled binary: CLI flags, environment
// variables, config file, banner, request logging and graceful shutdown.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var binaryPath string

func TestMain(m *testing.M) {
	root, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "onp-cli-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binaryPath = filepath.Join(dir, "ollama-nothink-proxy")

	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./cmd/ollama-nothink-proxy")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type runningProxy struct {
	base    string
	port    int
	cmd     *exec.Cmd
	stdout  *syncBuffer
	stderr  *syncBuffer
	done    chan struct{}
	waitErr error
}

// hasFlag reports whether args already set name (either --name value or
// --name=value).
func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

// testEnv builds a de-duplicated environment: inherited vars first, then
// resets, then the test's overrides. Duplicate keys would make getenv results
// platform-dependent.
func testEnv(overrides map[string]string) []string {
	environ := map[string]string{}
	for _, item := range os.Environ() {
		if k, v, ok := strings.Cut(item, "="); ok {
			environ[k] = v
		}
	}
	delete(environ, "PORT")
	delete(environ, "UPSTREAM")
	delete(environ, "PROXY_PARAMS")
	// Language: the assertions are written in Chinese, so pin the language
	// instead of inheriting the developer's locale. A test that wants English
	// passes ONP_LANG=en in overrides.
	delete(environ, "ONP_LANG")
	delete(environ, "LANG")
	delete(environ, "LC_ALL")
	delete(environ, "LC_MESSAGES")
	environ["ONP_LANG"] = "zh"
	for k, v := range overrides {
		environ[k] = v
	}
	out := make([]string, 0, len(environ))
	for k, v := range environ {
		out = append(out, k+"="+v)
	}
	return out
}

func startProxy(t *testing.T, args []string, env map[string]string) *runningProxy {
	t.Helper()
	// Tests supervise the process themselves: force foreground and never open
	// a browser. The default background mode has its own test.
	args = append([]string{"--foreground", "--no-browser"}, args...)
	// Never collide with a proxy the developer is already running on 11500.
	if !hasFlag(args, "--port") && env["PORT"] == "" {
		args = append(args, "--port", strconv.Itoa(freePort(t)))
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = testEnv(env)
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start proxy: %v", err)
	}

	p := &runningProxy{
		cmd:    cmd,
		stdout: stdout,
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() { p.stop(t) })

	// Discover the port from the banner and wait until it accepts connections.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("proxy exited early: %v\nstdout:\n%s\nstderr:\n%s", p.waitErr, stdout.String(), stderr.String())
		default:
		}
		if port := portFromBanner(stdout.String()); port != 0 {
			p.port = port
			p.base = fmt.Sprintf("http://127.0.0.1:%d", port)
			if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
				conn.Close()
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy did not become ready\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	return nil
}

func portFromBanner(banner string) int {
	for _, line := range strings.Split(banner, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "listen") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		addr := fields[1]
		_, portStr, err := net.SplitHostPort(strings.TrimPrefix(addr, "http://"))
		if err != nil {
			return 0
		}
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			return 0
		}
		return port
	}
	return 0
}

func (p *runningProxy) stop(t *testing.T) {
	t.Helper()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (p *runningProxy) stopAndWait(t *testing.T) error {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case <-p.done:
		p.cmd.Process = nil
		return p.waitErr
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		return fmt.Errorf("proxy did not exit after SIGTERM")
	}
}

type echoUpstream struct {
	server *httptest.Server
}

func newEchoUpstream(t *testing.T) *echoUpstream {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"body":   string(body),
		})
	}))
	t.Cleanup(srv.Close)
	return &echoUpstream{server: srv}
}

type echoResult struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	Body   string `json:"body"`
}

func postJSON(t *testing.T, base, path, body string) (int, echoResult) {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out echoResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	return resp.StatusCode, out
}

func decodeBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode injected body %q: %v", raw, err)
	}
	return out
}

func TestVersion(t *testing.T) {
	out, err := exec.Command(binaryPath, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version: %v (%s)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "ollama-nothink-proxy 0.2.0" {
		t.Errorf("--version = %q", got)
	}
}

func TestHelp(t *testing.T) {
	out, err := exec.Command(binaryPath, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, needle := range []string{
		"--set", "--no-defaults", "--max-concurrent", "--max-body-size",
		"PROXY_PARAMS", "--version",
	} {
		if !strings.Contains(string(out), needle) {
			t.Errorf("--help is missing %q", needle)
		}
	}
}

func TestInvalidArgumentsExit2(t *testing.T) {
	for _, args := range [][]string{
		{"--upstream", "ftp://x"},
		{"--set", "temperature"},
		{"--port", "not-a-number"},
		{"--nope"},
	} {
		out, err := exec.Command(binaryPath, args...).CombinedOutput()
		var exitErr *exec.ExitError
		if err == nil {
			t.Errorf("%v: expected failure, output %s", args, out)
			continue
		}
		if !asExitError(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Errorf("%v: exit = %v, want 2 (%s)", args, err, out)
		}
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func TestBannerAndPassthrough(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
	}, nil)

	banner := p.stdout.String()
	for _, needle := range []string{
		"ollama-nothink-proxy v0.2.0",
		"listen    " + p.base,
		"upstream  " + up.server.URL,
		"inject    keep_alive=\"30m\", max_tokens=96, temperature=0.2, top_p=0.9",
		"强制关闭思考",
		"前台运行 (pid ",
	} {
		if !strings.Contains(banner, needle) {
			t.Errorf("banner missing %q:\n%s", needle, banner)
		}
	}

	status, echo := postJSON(t, p.base, "/api/show", `{"model":"m"}`)
	if status != http.StatusOK || echo.Body != `{"model":"m"}` {
		t.Errorf("passthrough status=%d body=%q", status, echo.Body)
	}
}

func TestEnvPortUpstreamAndConfigFile(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	port := freePort(t)
	configPath := filepath.Join(dir, "config.json")
	file := fmt.Sprintf(`{"upstream": %q, "timeout": 30, "params": {"temperature": 0.33}}`, up.server.URL)
	if err := os.WriteFile(configPath, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	// PORT from the environment, upstream + params from the config file.
	p := startProxy(t, []string{
		"--config", configPath,
		"--log", filepath.Join(dir, "proxy.log"),
	}, map[string]string{
		"PORT":         fmt.Sprint(port),
		"PROXY_PARAMS": `{"max_tokens": 42}`,
	})

	if p.port != port {
		t.Errorf("port = %d, want %d from env", p.port, port)
	}
	status, echo := postJSON(t, p.base, "/v1/chat/completions", `{"model":"m"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	got := decodeBody(t, echo.Body)
	if got["max_tokens"] != float64(42) {
		t.Errorf("max_tokens = %v, want 42 from PROXY_PARAMS", got["max_tokens"])
	}
	if _, present := got["temperature"]; present {
		t.Errorf("PROXY_PARAMS should replace config params, got %v", got)
	}
	if got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v", got["reasoning_effort"])
	}
}

func TestNoDefaultsAndCLISet(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
		"--no-defaults",
		"--set", "temperature=0",
		"--set", "max_tokens=7",
	}, nil)

	_, echo := postJSON(t, p.base, "/api/chat", `{"model":"m"}`)
	got := decodeBody(t, echo.Body)
	options, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %v", got["options"])
	}
	if len(options) != 2 || options["temperature"] != float64(0) || options["num_predict"] != float64(7) {
		t.Errorf("options = %v, want only temperature/num_predict", options)
	}
	if got["think"] != false {
		t.Errorf("think = %v", got["think"])
	}
	if _, present := got["keep_alive"]; present {
		t.Error("--no-defaults still injected keep_alive")
	}
}

func TestVerboseLoggingAndLogFile(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "proxy.log")
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", logPath,
		"-v",
	}, nil)

	_, _ = postJSON(t, p.base, "/api/tags", "")
	_ = p.stopAndWait(t)

	console := p.stdout.String() + p.stderr.String()
	if !strings.Contains(console, "POST /api/tags -> 200") {
		t.Errorf("verbose console missing request line:\n%s", console)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "POST /api/tags -> 200") {
		t.Errorf("log file missing request line:\n%s", logged)
	}
	if !strings.Contains(string(logged), "start listen=") || !strings.Contains(string(logged), "stop") {
		t.Errorf("log file missing lifecycle lines:\n%s", logged)
	}
	if strings.Contains(string(logged), "Authorization") {
		t.Error("log file must not contain credentials")
	}
}

func TestGracefulShutdown(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
	}, nil)

	if err := p.stopAndWait(t); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if !strings.Contains(p.stderr.String(), "bye") {
		t.Errorf("stderr = %q", p.stderr.String())
	}
}

func postRaw(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, data
}

func TestPortalServesPageAndAppliesConfigLive(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", configPath,
		"--log", filepath.Join(dir, "proxy.log"),
	}, nil)

	// The portal page is embedded in the binary, not read from disk.
	resp, err := http.Get(p.base + "/__portal")
	if err != nil {
		t.Fatalf("portal page: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("portal status = %d", resp.StatusCode)
	}
	for _, needle := range []string{"关闭服务", "恢复默认", "logPause", "每秒刷新"} {
		if !strings.Contains(string(page), needle) {
			t.Errorf("page missing %q", needle)
		}
	}

	resp, err = http.Get(p.base + "/__portal/api/state")
	if err != nil {
		t.Fatalf("portal state: %v", err)
	}
	var state map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	resp.Body.Close()
	if state["upstream"] != up.server.URL {
		t.Errorf("state upstream = %v, want %s", state["upstream"], up.server.URL)
	}
	if state["launch_ready"] != true {
		t.Errorf("launch_ready = %v", state["launch_ready"])
	}

	// Saving params through the portal changes injection immediately.
	status, data := postRaw(t, p.base+"/__portal/api/config", `{"params":{"temperature":0.25}}`)
	if status != http.StatusOK {
		t.Fatalf("config save status = %d: %s", status, data)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["params"].(map[string]any)["temperature"] != 0.25 {
		t.Errorf("saved params = %v", saved["params"])
	}

	_, echo := postJSON(t, p.base, "/v1/chat/completions", `{"model":"m"}`)
	got := decodeBody(t, echo.Body)
	if got["temperature"] != 0.25 {
		t.Errorf("injected temperature = %v, want 0.25", got["temperature"])
	}
	if got["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v", got["reasoning_effort"])
	}

	// The config file was persisted in the documented shape.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document["params"].(map[string]any)["temperature"] != 0.25 {
		t.Errorf("persisted params = %v", document["params"])
	}

	// Structured log entries are visible to the portal.
	resp, err = http.Get(p.base + "/__portal/api/log?since=0")
	if err != nil {
		t.Fatalf("log endpoint: %v", err)
	}
	var logs map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	entries, _ := logs["entries"].([]any)
	found := false
	for _, item := range entries {
		entry := item.(map[string]any)
		if entry["method"] == "POST" && entry["note"] == "injected:v1" {
			found = true
		}
	}
	if !found {
		t.Errorf("no injected:v1 request entry in %v", entries)
	}
}

func parseChildPID(banner string) int {
	marker := "(pid "
	index := strings.Index(banner, marker)
	if index < 0 {
		return 0
	}
	rest := banner[index+len(marker):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return 0
	}
	pid, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return pid
}

func waitForState(t *testing.T, base string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/__portal/api/state")
		if err == nil {
			var state map[string]any
			err = json.NewDecoder(resp.Body).Decode(&state)
			resp.Body.Close()
			if err == nil && state["version"] != nil {
				return state
			}
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("portal did not become ready: %v", lastErr)
	return nil
}

func waitForPortClosed(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d still accepting connections", port)
}

func TestBackgroundModeSpawnsAndPortalShutsDown(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	port := freePort(t)
	configPath := filepath.Join(dir, "config.json")
	logPath := filepath.Join(dir, "proxy.log")

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath,
		"--port", strconv.Itoa(port),
		"--upstream", up.server.URL,
		"--config", configPath,
		"--log", logPath,
		"--no-browser",
	)
	cmd.Env = testEnv(nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("background start failed: %v\n%s", err, out)
	}
	banner := string(out)
	if !strings.Contains(banner, "后台运行 (pid ") {
		t.Fatalf("banner does not report a background pid:\n%s", banner)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !strings.Contains(banner, "portal    "+base+"/__portal") {
		t.Errorf("banner missing portal URL:\n%s", banner)
	}

	pid := parseChildPID(banner)
	if pid == 0 {
		t.Fatalf("could not parse child pid from:\n%s", banner)
	}
	t.Cleanup(func() {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	})

	state := waitForState(t, base, 10*time.Second)
	if state["pid"] != float64(pid) {
		t.Errorf("state pid = %v, want %d", state["pid"], pid)
	}
	if state["stopping"] != false {
		t.Errorf("stopping = %v", state["stopping"])
	}

	// Background mode must not open a browser when --no-browser is set; the
	// parent has already exited by now.
	if !strings.Contains(banner, "停止服务") {
		t.Errorf("banner missing stop hint:\n%s", banner)
	}

	status, data := postRaw(t, base+"/__portal/api/shutdown", "")
	if status != http.StatusOK || !strings.Contains(string(data), `"ok":true`) {
		t.Fatalf("shutdown status=%d body=%s", status, data)
	}
	waitForPortClosed(t, port, 10*time.Second)

	logged := readFileEventually(t, logPath, "stop", 5*time.Second)
	if !strings.Contains(logged, "start listen=") || !strings.Contains(logged, "stop") {
		t.Errorf("log missing lifecycle lines:\n%s", logged)
	}
}

func readFileEventually(t *testing.T, path, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), needle) {
			return string(data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestBackgroundModeReusesRunningInstance(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	port := freePort(t)
	args := []string{
		"--port", strconv.Itoa(port),
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
		"--no-browser",
	}

	first := exec.Command(binaryPath, args...)
	first.Env = testEnv(nil)
	out, err := first.CombinedOutput()
	if err != nil {
		t.Fatalf("first start: %v\n%s", err, out)
	}
	pid := parseChildPID(string(out))
	t.Cleanup(func() {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForState(t, base, 10*time.Second)

	second := exec.Command(binaryPath, args...)
	second.Env = testEnv(nil)
	out2, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("second start: %v\n%s", err, out2)
	}
	if !strings.Contains(string(out2), "复用已在运行的实例") {
		t.Errorf("second start did not reuse the instance:\n%s", out2)
	}
	if strings.Contains(string(out2), "后台 (pid ") {
		t.Errorf("second start spawned another process:\n%s", out2)
	}

	postRaw(t, base+"/__portal/api/shutdown", "")
	waitForPortClosed(t, port, 10*time.Second)
}

func TestRestartReplacesRunningInstance(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	args := []string{
		"--port", strconv.Itoa(port),
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
		"--no-browser",
	}

	start := func(extra ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		argv := append(append([]string{}, extra...), args...)
		cmd := exec.CommandContext(ctx, binaryPath, argv...)
		cmd.Env = testEnv(nil)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("start failed: %v\n%s", err, out)
		}
		return string(out)
	}

	firstBanner := start()
	firstPID := parseChildPID(firstBanner)
	if firstPID == 0 {
		t.Fatalf("could not parse first pid:\n%s", firstBanner)
	}
	t.Cleanup(func() {
		// Kill whatever is listening now; the pid changed during the test.
		if inst, ok := inspectState(base); ok {
			if pid, isNum := inst["pid"].(float64); isNum {
				if proc, err := os.FindProcess(int(pid)); err == nil {
					_ = proc.Signal(syscall.SIGTERM)
				}
			}
		}
	})

	// restart must stop the running instance and bring up a new process.
	banner := start("restart")
	secondPID := parseChildPID(banner)
	if secondPID == 0 {
		t.Fatalf("banner does not report a pid after restart:\n%s", banner)
	}
	if secondPID == firstPID {
		t.Fatalf("restart kept the same pid %d:\n%s", firstPID, banner)
	}
	if !strings.Contains(banner, "已停掉旧实例") {
		t.Errorf("banner missing restart note:\n%s", banner)
	}

	state := waitForState(t, base, 10*time.Second)
	if state["pid"] != float64(secondPID) {
		t.Errorf("state pid = %v, want %d", state["pid"], secondPID)
	}
	// The new instance serves requests.
	status, echo := postJSON(t, base, "/api/show", `{"model":"m"}`)
	if status != http.StatusOK || echo.Body != `{"model":"m"}` {
		t.Errorf("passthrough after restart status=%d body=%q", status, echo.Body)
	}
}

func TestRestartStartsWhenNothingIsRunning(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath,
		"restart",
		"--port", strconv.Itoa(port),
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
		"--no-browser",
	)
	cmd.Env = testEnv(nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restart failed: %v\n%s", err, out)
	}
	banner := string(out)
	if !strings.Contains(banner, "没有在运行的实例") {
		t.Errorf("banner missing 'nothing running' note:\n%s", banner)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	state := waitForState(t, base, 10*time.Second)
	pid, _ := state["pid"].(float64)
	if pid == 0 {
		t.Fatalf("state pid = %v", state["pid"])
	}
	t.Cleanup(func() {
		if proc, err := os.FindProcess(int(pid)); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	})
	postRaw(t, base+"/__portal/api/shutdown", "")
	waitForPortClosed(t, port, 10*time.Second)
}

// inspectState reads /api/state without failing the test when nothing answers.
func inspectState(base string) (map[string]any, bool) {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(base + "/__portal/api/state")
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	var state map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, false
	}
	return state, true
}

// TestEnglishBannerAndHelp covers the English side of the language switch:
// --lang on the command line and ONP_LANG in the environment.
func TestEnglishBannerAndHelp(t *testing.T) {
	out, err := exec.Command(binaryPath, "--lang", "en", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--lang en --help: %v (%s)", err, out)
	}
	for _, needle := range []string{
		"Usage: ollama-nothink-proxy", "start the proxy", "--max-body-size", "ONP_LANG",
	} {
		if !strings.Contains(string(out), needle) {
			t.Errorf("English --help missing %q:\n%s", needle, out)
		}
	}
	if strings.Contains(string(out), "用法:") {
		t.Error("English --help still contains the Chinese usage line")
	}

	up := newEchoUpstream(t)
	dir := t.TempDir()
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", filepath.Join(dir, "config.json"),
		"--log", filepath.Join(dir, "proxy.log"),
	}, map[string]string{"ONP_LANG": "en"})

	banner := p.stdout.String()
	for _, needle := range []string{
		"foreground (pid ",
		"+ thinking forced off",
		"concurrency 8",
		"settings & logs",
		"Ctrl-C (or kill ",
	} {
		if !strings.Contains(banner, needle) {
			t.Errorf("English banner missing %q:\n%s", needle, banner)
		}
	}
	for _, unwanted := range []string{"强制关闭思考", "前台运行", "停止服务"} {
		if strings.Contains(banner, unwanted) {
			t.Errorf("English banner still contains %q:\n%s", unwanted, banner)
		}
	}

	// The page is served in English too: the language tag is the hint the
	// client-side dictionary reads on the first paint.
	page := getPage(t, p.base+"/__portal")
	if !strings.Contains(page, `<html lang="en">`) {
		t.Error("portal page was not seeded with lang=en")
	}
	if strings.Contains(page, "__ONP_LANG__") {
		t.Error("portal page still contains the language placeholder")
	}

	// A rejected --lang is an error, not a silent fallback. The invalid
	// upstream is only there so a regression exits instead of serving.
	out, err = exec.Command(binaryPath, "--lang", "fr", "--upstream", "ftp://x", "--no-browser").CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil || !asExitError(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("--lang fr exit = %v (%s)", err, out)
	}
	// The rejected value is reported in the fallback language, so only the
	// exit code and the value itself are language-independent assertions.
	if !strings.Contains(string(out), `"fr"`) {
		t.Errorf("--lang fr output = %s", out)
	}
}

func getPage(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}

// TestLanguageSwitchThroughPortal mirrors what the page's EN / 中文 button
// does: POST the language, expect the config file, the state, the page and
// later log lines to follow.
func TestLanguageSwitchThroughPortal(t *testing.T) {
	up := newEchoUpstream(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	p := startProxy(t, []string{
		"--upstream", up.server.URL,
		"--config", configPath,
		"--log", filepath.Join(dir, "proxy.log"),
	}, nil)

	state := waitForState(t, p.base, 5*time.Second)
	if state["lang"] != "zh" {
		t.Fatalf("initial lang = %v, want zh", state["lang"])
	}

	status, data := postRaw(t, p.base+"/__portal/api/config", `{"lang":"en"}`)
	if status != http.StatusOK {
		t.Fatalf("config save status = %d: %s", status, data)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["lang"] != "en" {
		t.Errorf("saved lang = %v, want en", saved["lang"])
	}

	// Persisted, so a restart keeps the choice.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	if !strings.Contains(string(raw), `"lang": "en"`) {
		t.Errorf("config file has no lang: %s", raw)
	}

	if page := getPage(t, p.base+"/__portal"); !strings.Contains(page, `<html lang="en">`) {
		t.Error("page did not switch to lang=en")
	}

	// Rejected paths are logged, and the log line follows the new language.
	resp, err := http.Post(p.base+"//bad", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("bad path request: %v", err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		logs := getPage(t, p.base+"/__portal/api/log?since=0")
		if strings.Contains(logs, "invalid request path") {
			break
		}
		if strings.Contains(logs, "非法路径") {
			t.Fatalf("log line is still Chinese after the switch: %s", logs)
		}
		if time.Now().After(deadline) {
			t.Fatalf("English log line never appeared: %s", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
