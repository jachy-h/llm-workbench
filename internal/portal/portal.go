// Package portal serves the local-only /__portal UI: status, live logs,
// injection params, macOS login autostart and a shutdown button.
//
// The page is embedded into the binary with //go:embed, so a single file is
// all a user needs. Every route is restricted to loopback clients because the
// write endpoints can change config, install a LaunchAgent or stop the proxy.
package portal

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/jachy-h/llm-workbench/internal/autostart"
	"github.com/jachy-h/llm-workbench/internal/config"
	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/logstore"
	"github.com/jachy-h/llm-workbench/internal/netutil"
)

const (
	// Prefix is the mount point of the portal.
	Prefix = "/__portal"
	// MaxBody bounds portal request bodies (config JSON).
	MaxBody = 256 * 1024
	// PollMS advertises how often the page polls for new log entries.
	PollMS = 1000
	// langPlaceholder marks where the page wants the current language tag, so
	// the first paint is already in the right language.
	langPlaceholder = "__LLM_WORKBENCH_LANG__"
)

//go:embed portal.html
var pageHTML []byte

// Defaults is the built-in configuration shown by "恢复默认".
type Defaults struct {
	Host     string                     `json:"host"`
	Port     int                        `json:"port"`
	Upstream string                     `json:"upstream"`
	Timeout  float64                    `json:"timeout"`
	Params   map[string]json.RawMessage `json:"params"`
}

// Options wires the portal to the running proxy.
type Options struct {
	Version string
	PID     int
	Listen  string
	Config  *config.Config
	Log     *logstore.Log
	// Defaults is what "恢复默认" fills in (the built-in values, not the
	// currently saved ones).
	Defaults Defaults
	// LaunchCommand returns the argv the LaunchAgent should run. It is a
	// function so a portal config change is reflected at install time.
	LaunchCommand func() []string
	Workdir       string
	// Stopping is shared with the server so state can report shutdown.
	Stopping *atomic.Bool
	// Stop initiates graceful shutdown (usually http.Server.Shutdown).
	Stop func()
}

// Handler serves the portal routes.
type Handler struct {
	opts Options
}

// New builds a portal handler.
func New(opts Options) *Handler {
	return &Handler{opts: opts}
}

// Handle serves the request and reports whether it belonged to the portal.
// When it returns false the caller must forward the request to Ollama.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path != Prefix && !strings.HasPrefix(path, Prefix+"/") {
		return false
	}
	if !netutil.Guard(r) {
		writeJSONError(w, http.StatusForbidden, i18n.T("portal.errLoopback"))
		return true
	}

	route := strings.TrimSuffix(strings.TrimPrefix(path, Prefix), "/")
	if route == "" {
		route = "/"
	}

	switch {
	case route == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.servePage(w, r)
	case route == "/api/state" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, h.state())
	case route == "/api/config" && r.Method == http.MethodPost:
		h.saveConfig(w, r)
	case route == "/api/autostart" && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		h.toggleAutostart(w, r.Method == http.MethodPost)
	case route == "/api/shutdown" && r.Method == http.MethodPost:
		h.shutdown(w)
	case route == "/api/log" && r.Method == http.MethodGet:
		h.logEntries(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, i18n.T("portal.errNotFound", path))
	}
	return true
}

// DefaultsFor builds the "restore defaults" payload from built-in constants.
func DefaultsFor() Defaults {
	return Defaults{
		Host:     config.DefaultHost,
		Port:     config.DefaultPort,
		Upstream: config.DefaultUpstream,
		Timeout:  config.DefaultTimeout.Seconds(),
		Params:   config.DefaultParams(),
	}
}

type stateResponse struct {
	Version     string                     `json:"version"`
	PID         int                        `json:"pid"`
	Listen      string                     `json:"listen"`
	Upstream    string                     `json:"upstream"`
	Timeout     float64                    `json:"timeout"`
	Params      map[string]json.RawMessage `json:"params"`
	LogPath     string                     `json:"log_path"`
	LogCapacity int                        `json:"log_capacity"`
	ConfigPath  string                     `json:"config_path"`
	LaunchReady bool                       `json:"launch_ready"`
	Stopping    bool                       `json:"stopping"`
	Lang        string                     `json:"lang"`
	Defaults    Defaults                   `json:"defaults"`
	Autostart   autostart.State            `json:"autostart"`
}

type autostartResponse struct {
	stateResponse
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type logResponse struct {
	Path     string           `json:"path"`
	Entries  []logstore.Entry `json:"entries"`
	Oldest   int64            `json:"oldest"`
	Latest   int64            `json:"latest"`
	Capacity int              `json:"capacity"`
	PollMS   int              `json:"poll_ms"`
	// Text keeps the old plain-text shape working.
	Text string `json:"text"`
}

func (h *Handler) state() stateResponse {
	cfg := h.opts.Config
	snap := cfg.Snapshot()
	return stateResponse{
		Version:     h.opts.Version,
		PID:         h.opts.PID,
		Listen:      h.opts.Listen,
		Upstream:    snap.Upstream,
		Timeout:     snap.Timeout.Seconds(),
		Params:      snap.Params,
		LogPath:     cfg.LogPath,
		LogCapacity: h.logCapacity(),
		ConfigPath:  cfg.ConfigPath,
		LaunchReady: h.launchReady(),
		Stopping:    h.stopping(),
		Lang:        cfg.Lang().String(),
		Defaults:    h.opts.Defaults,
		Autostart:   autostart.StateNow(),
	}
}

func (h *Handler) launchCommand() []string {
	if h.opts.LaunchCommand == nil {
		return nil
	}
	return h.opts.LaunchCommand()
}

func (h *Handler) launchReady() bool {
	return len(h.launchCommand()) > 0
}

func (h *Handler) logCapacity() int {
	if h.opts.Log == nil {
		return logstore.DefaultCapacity
	}
	return h.opts.Log.Capacity()
}

func (h *Handler) stopping() bool {
	return h.opts.Stopping != nil && h.opts.Stopping.Load()
}

func (h *Handler) servePage(w http.ResponseWriter, r *http.Request) {
	page := pageHTML
	if h.opts.Config != nil {
		page = bytes.ReplaceAll(page, []byte(langPlaceholder), []byte(h.opts.Config.Lang().String()))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}

func (h *Handler) saveConfig(w http.ResponseWriter, r *http.Request) {
	payload, ok := readJSONObject(w, r)
	if !ok {
		return
	}
	if err := h.opts.Config.Apply(payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.state())
}

func (h *Handler) toggleAutostart(w http.ResponseWriter, install bool) {
	var result autostart.Result
	if install {
		result = autostart.Install(h.launchCommand(), h.opts.Config.LogPath, h.opts.Workdir)
	} else {
		result = autostart.Remove()
	}

	body := autostartResponse{
		stateResponse: h.state(),
		OK:            result.OK,
		Message:       result.Message,
	}
	body.Autostart = result.State
	writeJSON(w, http.StatusOK, body)

	if install {
		h.writeLog(i18n.T("portal.logAutostartAdd", result.Message), "text")
	} else {
		h.writeLog(i18n.T("portal.logAutostartDrop", result.Message), "text")
	}
}

func (h *Handler) shutdown(w http.ResponseWriter) {
	if h.stopping() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": i18n.T("portal.alreadyStopping")})
		return
	}
	if h.opts.Stopping != nil {
		h.opts.Stopping.Store(true)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": i18n.T("portal.shutdownDone")})
	h.writeLog(i18n.T("portal.logShutdown"), "life")
	if h.opts.Stop != nil {
		go h.opts.Stop()
	}
}

func (h *Handler) logEntries(w http.ResponseWriter, r *http.Request) {
	since := queryInt64(r, "since", 0)
	limit := queryInt(r, "lines", 0)
	if h.opts.Log == nil {
		writeJSON(w, http.StatusOK, logResponse{PollMS: PollMS})
		return
	}

	data := h.opts.Log.Entries(since)
	entries := data.Entries
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	texts := make([]string, 0, len(entries))
	for _, entry := range entries {
		texts = append(texts, entry.Text)
	}
	writeJSON(w, http.StatusOK, logResponse{
		Path:     h.opts.Log.Path(),
		Entries:  entries,
		Oldest:   data.Oldest,
		Latest:   data.Latest,
		Capacity: data.Capacity,
		PollMS:   PollMS,
		Text:     strings.Join(texts, "\n"),
	})
}

func (h *Handler) writeLog(text, kind string) {
	if h.opts.Log == nil {
		return
	}
	level := ""
	if kind == "life" {
		level = "warn"
	}
	h.opts.Log.Write(text, level, kind)
}

// readJSONObject reads a bounded JSON object body, replying 400 on failure.
func readJSONObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, i18n.T("portal.errReadBody", err))
		return nil, false
	}
	if len(raw) > MaxBody {
		writeJSONError(w, http.StatusRequestEntityTooLarge, i18n.T("portal.errBodyTooLarge"))
		return nil, false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]json.RawMessage{}, true
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		var anyValue any
		if json.Unmarshal(raw, &anyValue) == nil {
			writeJSONError(w, http.StatusBadRequest, i18n.T("portal.errBodyObject"))
		} else {
			writeJSONError(w, http.StatusBadRequest, i18n.T("portal.errBodyJSON", err))
		}
		return nil, false
	}
	return payload, true
}

func queryInt64(r *http.Request, key string, fallback int64) int64 {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]map[string]string{"error": {"message": message}})
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
