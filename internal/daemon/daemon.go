// Package daemon implements the default background mode: re-exec the proxy
// detached from the terminal, wait until its portal answers, and hand the URL
// to the browser.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jachy-h/llm-workbench/internal/i18n"
)

// Result describes how the background start went.
type Result struct {
	// Reused is true when an instance already answered on the portal URL.
	Reused bool
	// PID is the spawned child (0 when Reused).
	PID int
}

// ErrNotRunning is returned by Stop when nothing answers on the portal URL.
// It is a sentinel: callers compare it with errors.Is and print their own
// message.
var ErrNotRunning = errors.New("daemon: no running instance")

// Instance is the part of /api/state the CLI needs.
type Instance struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

// Inspect reads /api/state and reports whether a proxy is listening.
func Inspect(portalURL string, timeout time.Duration) (Instance, bool) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(portalURL + "/api/state")
	if err != nil {
		return Instance{}, false
	}
	defer resp.Body.Close()

	var instance Instance
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&instance); err != nil {
		return Instance{}, false
	}
	if instance.Version == "" {
		return Instance{}, false
	}
	return instance, true
}

// Probe reports whether a proxy already answers on portalURL.
func Probe(portalURL string, timeout time.Duration) bool {
	_, ok := Inspect(portalURL, timeout)
	return ok
}

// Stop asks a running proxy to shut down and waits until it stops answering.
// It returns ErrNotRunning when no instance was listening in the first place.
func Stop(portalURL string, waitTimeout time.Duration) error {
	if !Probe(portalURL, time.Second) {
		return ErrNotRunning
	}

	request, err := http.NewRequest(http.MethodPost, portalURL+"/api/shutdown", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(request); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	// A dropped connection is fine too: the process may exit before replying.

	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if !Probe(portalURL, 400*time.Millisecond) {
			return nil
		}
		time.Sleep(120 * time.Millisecond)
	}
	return fmt.Errorf(i18n.T("daemon.errStopTimeout"), waitTimeout)
}

// Run starts the proxy in the background unless one is already listening.
func Run(args []string, portalURL, logPath string, waitTimeout time.Duration) (Result, error) {
	if Probe(portalURL, time.Second) {
		return Result{Reused: true}, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return Result{}, fmt.Errorf(i18n.T("daemon.errExecutable"), err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return Result{}, fmt.Errorf(i18n.T("daemon.errLogDir"), err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf(i18n.T("daemon.errLogFile"), err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, childArgs(args)...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf(i18n.T("daemon.errChild"), err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New(i18n.T("daemon.errChildExited"))
			}
			return Result{}, err
		default:
		}
		if Probe(portalURL, 500*time.Millisecond) {
			return Result{PID: cmd.Process.Pid}, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return Result{}, fmt.Errorf(i18n.T("daemon.errNoResponse"), waitTimeout)
}

// childArgs keeps the caller's flags but forces foreground + no browser, so
// the detached child is the actual server (and does not open a second tab).
func childArgs(args []string) []string {
	out := make([]string, 0, len(args)+2)
	for _, arg := range args {
		switch {
		case arg == "-f", arg == "--foreground", arg == "--no-browser":
			continue
		case strings.HasPrefix(arg, "--foreground="), strings.HasPrefix(arg, "--no-browser="):
			continue
		}
		out = append(out, arg)
	}
	return append(out, "--foreground", "--no-browser")
}

// OpenBrowser launches the platform browser without blocking the proxy.
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}
