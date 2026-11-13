// Package autostart manages the macOS launchd LaunchAgent that starts the
// proxy at login. On other platforms Supported reports false and the portal
// disables the corresponding buttons.
package autostart

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jachy-h/ollama-nothink-proxy/internal/i18n"
)

// Label is the launchd job label and plist base name.
const Label = "com.github.ollama-nothink-proxy"

const launchctlTimeout = 15 * time.Second

// State is what the portal shows for the autostart card.
type State struct {
	Supported bool   `json:"supported"`
	Label     string `json:"label"`
	PlistPath string `json:"plist_path"`
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
}

// Result is State plus the outcome message of an install/remove action.
type Result struct {
	State
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Supported reports whether this platform uses launchd.
func Supported() bool { return runtime.GOOS == "darwin" }

// PlistPath is ~/Library/LaunchAgents/<Label>.plist.
func PlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

// State reports the current install/load status.
func StateNow() State {
	path := PlistPath()
	state := State{
		Supported: Supported(),
		Label:     Label,
		PlistPath: path,
		Installed: fileExists(path),
	}
	state.Loaded = loaded()
	return state
}

func loaded() bool {
	if !Supported() {
		return false
	}
	if code, _, err := launchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)); err == nil && code == 0 {
		return true
	}
	if code, _, err := launchctl("list", Label); err == nil && code == 0 {
		return true
	}
	return false
}

// JobPID reports the pid launchd runs for this job. Restart uses it to tell
// whether the instance answering on a port really is the supervised one.
func JobPID() (int, bool) {
	if !Supported() {
		return 0, false
	}
	code, output, err := launchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label))
	if err != nil || code != 0 {
		return 0, false
	}
	return parseJobPID(output)
}

// parseJobPID pulls "pid = 1234" out of `launchctl print` output. A loaded
// job that is not running has no pid line, and a crashed one reports 0.
func parseJobPID(output string) (int, bool) {
	for _, raw := range strings.Split(output, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(raw), "pid = ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || pid <= 0 {
			return 0, false
		}
		return pid, true
	}
	return 0, false
}

// Install writes the plist and loads it. The command is the argv launchd
// should run (normally the proxy binary with --foreground --no-browser).
func Install(command []string, logPath, workingDir string) Result {
	state := StateNow()
	if !state.Supported {
		return Result{State: state, OK: false, Message: i18n.T("autostart.errOnlyMac")}
	}
	if len(command) == 0 {
		return Result{State: state, OK: false, Message: i18n.T("autostart.errNoCommand")}
	}

	payload := RenderPlist(command, logPath, workingDir)
	if state.Installed && state.Loaded && samePlist(state.PlistPath, payload) {
		return Result{State: state, OK: true, Message: i18n.T("autostart.upToDate")}
	}

	// A loaded job must be booted out before bootstrap, otherwise launchd
	// answers "already loaded".
	bootout()

	if err := os.MkdirAll(filepath.Dir(state.PlistPath), 0o755); err != nil {
		return Result{State: StateNow(), OK: false, Message: i18n.T("autostart.errWritePlist", err)}
	}
	if err := os.WriteFile(state.PlistPath, []byte(payload), 0o644); err != nil {
		return Result{State: StateNow(), OK: false, Message: i18n.T("autostart.errWritePlist", err)}
	}

	loadErr := bootstrap(state.PlistPath)
	time.Sleep(300 * time.Millisecond)
	latest := StateNow()
	if loadErr != "" {
		return Result{State: latest, OK: false, Message: i18n.T("autostart.errLoad", loadErr)}
	}
	return Result{State: latest, OK: true, Message: i18n.T("autostart.installed")}
}

// Remove unloads the job and deletes the plist.
func Remove() Result {
	state := StateNow()
	if !state.Supported {
		return Result{State: state, OK: false, Message: i18n.T("autostart.errOnlyMac")}
	}

	bootout()
	if err := os.Remove(state.PlistPath); err != nil && !os.IsNotExist(err) {
		return Result{State: StateNow(), OK: false, Message: i18n.T("autostart.errRemovePlist", err)}
	}

	time.Sleep(300 * time.Millisecond)
	latest := StateNow()
	if latest.Installed || latest.Loaded {
		return Result{State: latest, OK: false, Message: i18n.T("autostart.errStillLoaded")}
	}
	return Result{State: latest, OK: true, Message: i18n.T("autostart.removed")}
}

// Kickstart asks launchd to restart a loaded job in place. This is the only
// safe way to restart an instance launchd supervises: with KeepAlive set,
// killing the process would just make launchd start it again behind our back.
func Kickstart() error {
	state := StateNow()
	if !state.Supported {
		return errors.New(i18n.T("autostart.errOnlyMac"))
	}
	if !state.Loaded {
		return errors.New(i18n.T("autostart.notLoaded"))
	}
	code, output, err := launchctl("kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label))
	if err != nil {
		return err
	}
	if code != 0 {
		if output == "" {
			output = fmt.Sprintf("launchctl exit code %d", code)
		}
		return errors.New(output)
	}
	return nil
}

// RenderPlist produces the LaunchAgent property list. It is exported so the
// payload can be tested without touching launchd.
func RenderPlist(command []string, logPath, workingDir string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(comment(i18n.T("autostart.plistComment")))
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeKeyString(&b, "Label", Label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range command {
		b.WriteString("\t\t<string>" + escape(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n")
	// launchd does not inherit the shell locale, so pin the language of the
	// supervised instance to the one this UI runs in.
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	writeKeyString(&b, "ONP_LANG", i18n.Current().String())
	b.WriteString("\t</dict>\n")
	writeKeyString(&b, "StandardOutPath", logPath)
	writeKeyString(&b, "StandardErrorPath", logPath)
	if workingDir != "" {
		writeKeyString(&b, "WorkingDirectory", workingDir)
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// comment renders text as one XML comment line. "--" would end the comment
// early, so it is spaced out; a trailing "-" would too, so it is dropped.
func comment(text string) string {
	// One pass is not enough: "---" turns into "- --", which still closes the
	// comment early. Each pass shortens the dash runs, so this terminates.
	for strings.Contains(text, "--") {
		text = strings.ReplaceAll(text, "--", "- -")
	}
	text = strings.TrimRight(text, "-")
	return "<!-- " + text + " -->\n"
}

func writeKeyString(b *strings.Builder, key, value string) {
	b.WriteString("\t<key>" + escape(key) + "</key>\n")
	b.WriteString("\t<string>" + escape(value) + "</string>\n")
}

func escape(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

func samePlist(path, payload string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(data) == payload
}

// launchctl runs launchctl with a timeout. code is the exit status (0 when
// the command ran), output is combined stdout+stderr.
func launchctl(args ...string) (code int, output string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchctlTimeout)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if runErr != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(runErr, &exitErr); ok {
			return exitErr.ExitCode(), strings.TrimSpace(string(out)), nil
		}
		return 1, strings.TrimSpace(string(out)), runErr
	}
	return 0, strings.TrimSpace(string(out)), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func bootout() {
	for _, args := range [][]string{
		{"bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)},
		{"unload", "-w", PlistPath()},
	} {
		if code, _, err := launchctl(args...); err == nil && code == 0 {
			return
		}
	}
}

func bootstrap(path string) string {
	var errors []string
	for _, args := range [][]string{
		{"bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path},
		{"load", "-w", path},
	} {
		code, output, err := launchctl(args...)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}
		if code == 0 {
			return ""
		}
		errors = append(errors, output)
	}
	joined := strings.Join(nonEmpty(errors), "; ")
	if joined == "" {
		return i18n.T("autostart.errBootstrap")
	}
	return joined
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nonEmpty(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
