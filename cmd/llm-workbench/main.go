// Command llm-workbench is a local-first LLM proxy and experiment workbench.
// The proxy forces thinking off and fills in default generation params in
// front of Ollama, and ships a local portal for status, live logs and settings.
//
// It runs in the background by default; -f keeps it in the foreground for
// supervisors such as launchd or systemd. "restart" stops the instance that is
// already listening and starts a fresh one.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jachy-h/llm-workbench/internal/autostart"
	"github.com/jachy-h/llm-workbench/internal/config"
	"github.com/jachy-h/llm-workbench/internal/daemon"
	"github.com/jachy-h/llm-workbench/internal/engine"
	"github.com/jachy-h/llm-workbench/internal/experiments"
	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/inspector"
	"github.com/jachy-h/llm-workbench/internal/logstore"
	"github.com/jachy-h/llm-workbench/internal/portal"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/provider/ollama"
	"github.com/jachy-h/llm-workbench/internal/proxy"
	"github.com/jachy-h/llm-workbench/internal/storage"
	"github.com/jachy-h/llm-workbench/internal/workbench"
)

const startupWait = 20 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	command, rest := config.SplitCommand(args)
	if command == config.CommandMigrate {
		return runMigrate(rest, stdout, stderr)
	}
	cfg, err := config.ParseRuntime(rest, os.LookupEnv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, config.UsageText())
		return 0
	case errors.Is(err, config.ErrVersionRequested):
		fmt.Fprintf(stdout, "llm-workbench %s\n", config.Version)
		return 0
	case err != nil:
		fmt.Fprintf(stderr, i18n.T("cli.errParse")+"\n", err)
		fmt.Fprintln(stderr, i18n.T("cli.seeHelp"))
		return 2
	}

	portalURL := cfg.ListenURL() + portal.Prefix
	note := ""
	if command == config.CommandRestart {
		var code int
		note, code = restartInstance(cfg, portalURL, len(rest) > 0, stdout, stderr)
		if code != 0 {
			return code
		}
		if note == "" {
			// launchd restarted the job itself and the banner is already out.
			return 0
		}
	}
	if !cfg.Foreground {
		return runBackground(rest, cfg, portalURL, note, stdout, stderr)
	}
	return runForeground(cfg, portalURL, note, stdout, stderr)
}

// restartInstance stops whatever answers on the portal URL and reports a note
// for the banner of the start that follows. It returns an empty note when
// launchd handled the restart, and a non-zero code when restarting failed.
func restartInstance(cfg *config.Config, portalURL string, flagsGiven bool, stdout, stderr io.Writer) (string, int) {
	// A launchd job supervises its own process (KeepAlive), so stopping it
	// directly would race with launchd starting it again: ask launchd instead.
	// Only do that when the instance on this portal URL *is* the launchd job;
	// otherwise (e.g. a second instance on another port) stop and start.
	current, running := daemon.Inspect(portalURL, time.Second)
	if running && launchdOwns(current.PID) {
		if err := autostart.Kickstart(); err != nil {
			fmt.Fprintf(stderr, i18n.T("cli.errKickstart")+"\n", err)
			return "", 1
		}
		instance, ok := waitForRestart(portalURL, current, true, startupWait)
		if !ok {
			fmt.Fprintf(stderr, i18n.T("cli.errLaunchdWait")+"\n", portalURL)
			fmt.Fprintf(stderr, i18n.T("cli.hintLog")+"\n", prettyPath(cfg.LogPath))
			return "", 1
		}
		printBanner(stdout, cfg, portalURL, bannerOptions{
			Status:    i18n.T("banner.statusLaunchd", instance.PID),
			Reused:    true,
			Note:      i18n.T("banner.noteLaunchd"),
			Stop:      i18n.T("banner.stopPortal"),
			Workbench: cfg.ListenURL() + workbench.Prefix,
		})
		if flagsGiven {
			fmt.Fprintln(stdout, i18n.T("cli.tipPlist"))
		}
		if cfg.OpenBrowser {
			daemon.OpenBrowser(portalURL)
		}
		return "", 0
	}

	err := daemon.Stop(portalURL, startupWait)
	switch {
	case errors.Is(err, daemon.ErrNotRunning):
		return i18n.T("cli.noteNoInstance"), 0
	case err != nil:
		fmt.Fprintf(stderr, i18n.T("cli.errStopOld")+"\n", err)
		fmt.Fprintf(stderr, i18n.T("cli.hintLog")+"\n", prettyPath(cfg.LogPath))
		return "", 1
	}
	return i18n.T("cli.noteStoppedOld", portalURL), 0
}

// launchdOwns reports whether pid is the process of the loaded LaunchAgent.
func launchdOwns(pid int) bool {
	if pid <= 0 {
		return false
	}
	jobPID, ok := autostart.JobPID()
	return ok && jobPID == pid
}

// waitForRestart waits until the portal answers again with a process that is
// not the one we asked launchd to replace.
func waitForRestart(portalURL string, before daemon.Instance, hadBefore bool, timeout time.Duration) (daemon.Instance, bool) {
	deadline := time.Now().Add(timeout)
	sawDown := false
	for time.Now().Before(deadline) {
		instance, ok := daemon.Inspect(portalURL, 500*time.Millisecond)
		switch {
		case !ok:
			sawDown = true
		case sawDown || !hadBefore || instance.PID != before.PID:
			return instance, true
		}
		time.Sleep(120 * time.Millisecond)
	}
	return daemon.Instance{}, false
}

// runMigrate copies the pre-rename config to the new location and reports
// what still needs manual attention. It never starts the proxy or touches
// launchd.
func runMigrate(args []string, stdout, stderr io.Writer) int {
	i18n.Set(i18n.Detect(os.LookupEnv))
	// Parse first: it validates the old config (read as a fallback) and settles
	// the output language, including --lang.
	if _, err := config.ParseRuntime(args, os.LookupEnv); err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.errParse")+"\n", err)
		return 2
	}

	dst := config.ConfigPathFromArgs(args)
	result, err := config.ImportLegacyConfig(dst)
	if err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.errMigrate")+"\n", err)
		return 1
	}

	switch result.Reason {
	case "copied":
		fmt.Fprintf(stdout, i18n.T("cli.migrateCopied")+"\n", result.Destination)
	case "destination_exists":
		fmt.Fprintf(stdout, i18n.T("cli.migrateExists")+"\n", result.Destination)
	default:
		fmt.Fprintf(stdout, i18n.T("cli.migrateMissing")+"\n", result.Source)
	}

	if autostart.LegacyInstalled() {
		fmt.Fprintf(stdout, i18n.T("cli.migrateLegacyAgent")+"\n",
			autostart.LegacyPlistPath(), autostart.LegacyPlistPath())
	}

	fmt.Fprintln(stdout, i18n.T("cli.migrateDone"))
	return 0
}

// recorderFunc adapts logstore.Log (which returns the entry) to the small
// proxy.Recorder interface.
type recorderFunc func(method, path string, status int, note, client, detail string)

func (f recorderFunc) WriteRequest(method, path string, status int, note, client, detail string) {
	f(method, path, status, note, client, detail)
}

func runBackground(args []string, cfg *config.Config, portalURL, note string, stdout, stderr io.Writer) int {
	result, err := daemon.Run(args, portalURL, cfg.LogPath, startupWait)
	if err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.errBackground")+"\n", err)
		fmt.Fprintf(stderr, i18n.T("cli.hintLog")+"\n", prettyPath(cfg.LogPath))
		return 1
	}

	opts := bannerOptions{
		Status:    i18n.T("banner.statusBackground", result.PID),
		Note:      note,
		Stop:      i18n.T("banner.stopPortalOrKill", result.PID),
		Workbench: cfg.ListenURL() + workbench.Prefix,
	}
	if result.Reused {
		opts.Status = i18n.T("banner.statusReused")
		opts.Reused = true
		opts.Stop = i18n.T("banner.stopPortal")
	}
	printBanner(stdout, cfg, portalURL, opts)
	if cfg.OpenBrowser {
		daemon.OpenBrowser(portalURL)
	}
	return 0
}

func runForeground(cfg *config.Config, portalURL, note string, stdout, stderr io.Writer) int {
	logs := logstore.New(cfg.LogPath, logstore.DefaultCapacity)
	defer logs.Close()

	var store *storage.Store
	if opened, err := storage.Open(cfg.Storage().Path); err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.warnHistory")+"\n", err)
	} else {
		store = opened
		defer store.Close()
	}

	var capture *inspector.Inspector
	inspectorOpts := inspector.Options{
		Store:         store,
		Enabled:       store != nil && cfg.Inspector().Enabled,
		CaptureBodies: cfg.Inspector().CaptureBodies,
		MaxBodyBytes:  cfg.Inspector().MaxBodyBytes,
		RetentionDays: cfg.Inspector().RetentionDays,
		MaxRows:       cfg.Inspector().MaxRows,
		Logger:        log.New(stderr, "inspector: ", 0),
	}
	if created, err := inspector.New(inspectorOpts); err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.warnHistory")+"\n", err)
	} else {
		capture = created
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = capture.Close(ctx)
		}()
	}
	var captureRecorder inspect.Recorder
	if capture != nil && capture.Enabled() {
		captureRecorder = capture
	}

	var modelEngine *engine.Engine
	if upstream := cfg.UpstreamURL(); upstream != nil {
		if ollamaProvider, err := ollama.New(upstream, provider.Client(cfg.Timeout())); err != nil {
			fmt.Fprintf(stderr, i18n.T("cli.warnProvider")+"\n", err)
		} else if created, err := engine.New(map[string]provider.Provider{ollamaProvider.ID(): ollamaProvider}, ollamaProvider.ID()); err != nil {
			fmt.Fprintf(stderr, i18n.T("cli.warnProvider")+"\n", err)
		} else {
			modelEngine = created
		}
	}
	var experimentRunner *experiments.Runner
	if store != nil && modelEngine != nil {
		experimentRunner = experiments.New(store, modelEngine, capture, 2)
		defer experimentRunner.Shutdown()
	}

	handler, err := proxy.New(proxy.Options{
		Upstream:         cfg.UpstreamURL(),
		Settings:         cfg,
		MaxConcurrent:    cfg.MaxConcurrent,
		MaxBodySize:      cfg.MaxBodySize,
		Verbose:          cfg.Verbose,
		CaptureBodyLimit: cfg.Inspector().MaxBodyBytes,
		CaptureRecorder:  captureRecorder,
		Recorder: recorderFunc(func(method, path string, status int, note, client, detail string) {
			logs.WriteRequest(method, path, status, note, client, detail)
		}),
		Console:  stdout,
		ErrorLog: stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.errStart")+"\n", err)
		return 1
	}
	defer handler.Close()

	listener, err := net.Listen("tcp", cfg.ListenAddr())
	if err != nil {
		fmt.Fprintf(stderr, i18n.T("cli.errStart")+"\n", err)
		fmt.Fprintf(stderr, i18n.T("cli.hintAddrInUse")+"\n", cfg.ListenAddr())
		return 1
	}

	var stopping atomic.Bool
	var server *http.Server
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}

	portalHandler := portal.New(portal.Options{
		Version:       config.Version,
		PID:           os.Getpid(),
		Listen:        cfg.ListenURL(),
		Config:        cfg,
		Log:           logs,
		Defaults:      portal.DefaultsFor(),
		LaunchCommand: func() []string { return launchCommand(cfg) },
		Workdir:       workdir(),
		Stopping:      &stopping,
		Stop:          stop,
	})
	workbenchHandler := workbench.New(workbench.Options{
		Version:   config.Version,
		Config:    cfg,
		Store:     store,
		Inspector: capture,
		Engine:    modelEngine,
		Runner:    experimentRunner,
	})
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if portalHandler.Handle(w, r) {
			return
		}
		if workbenchHandler.Handle(w, r) {
			return
		}
		handler.ServeHTTP(w, r)
	})

	server = &http.Server{
		Addr:              cfg.ListenAddr(),
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          log.New(stderr, "http: ", 0),
		// ReadTimeout / WriteTimeout stay unset on purpose: a long generation
		// must not be cut off mid-stream.
	}

	printBanner(stdout, cfg, portalURL, bannerOptions{
		Status:    i18n.T("banner.statusForeground", os.Getpid()),
		Note:      note,
		Stop:      i18n.T("banner.stopForeground", os.Getpid()),
		Workbench: cfg.ListenURL() + workbench.Prefix,
	})
	logs.Write(i18n.T("cli.logStart", cfg.ListenAddr(), cfg.Upstream(), cfg.ParamSummary()), "ok", "life")
	if cfg.OpenBrowser {
		daemon.OpenBrowser(portalURL)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if capture != nil && cfg.Inspector().Enabled {
		go func() {
			_, _ = capture.Maintain(ctx)
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_, _ = capture.Maintain(ctx)
				}
			}
		}()
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	code := 0
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, i18n.T("cli.errServe")+"\n", err)
			code = 1
		}
	case <-ctx.Done():
		stopping.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		fmt.Fprintln(stderr, "bye")
	}

	logs.Write("stop", "warn", "life")
	return code
}

// launchCommand is the argv a LaunchAgent should run: the proxy in the
// foreground so launchd supervises the real process.
func launchCommand(cfg *config.Config) []string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return nil
	}
	args := []string{
		exe,
		"--host", cfg.Host,
		"--port", strconv.Itoa(cfg.Port),
		"--upstream", cfg.Upstream(),
		"--timeout", strconv.FormatFloat(cfg.Timeout().Seconds(), 'f', -1, 64),
		"--config", cfg.ConfigPath,
		"--log", cfg.LogPath,
		"--db", cfg.Storage().Path,
		"--retention-days", strconv.Itoa(cfg.Inspector().RetentionDays),
		"--max-rows", strconv.Itoa(cfg.Inspector().MaxRows),
		"--lang", cfg.Lang().String(),
		"--thinking", string(cfg.Thinking()),
		"--foreground",
		"--no-browser",
	}
	if !cfg.Inspector().Enabled {
		args = append(args, "--no-inspector")
	}
	if cfg.Inspector().CaptureBodies {
		args = append(args, "--capture-bodies")
	}
	if profile := cfg.ActiveProfile(); profile != "" {
		args = append(args, "--profile", profile)
	}
	return args
}

func workdir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}
