// Package proxy implements the transparent HTTP proxy in front of Ollama.
//
// Requests are only rewritten on the two chat paths; everything else is
// forwarded as-is and responses are always streamed, never buffered.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/policy"
)

const (
	// defaultAcquireTimeout is how long a request waits for a free slot
	// before the proxy answers 503.
	defaultAcquireTimeout = 2 * time.Second

	defaultMaxConcurrent = 8
	defaultMaxBodySize   = int64(1 << 20)

	maxIdleConns        = 16
	idleConnTimeout     = 90 * time.Second
	upstreamDialTimeout = 30 * time.Second
)

// Injection notes are machine tokens rather than prose: the portal colours
// them and logstore matches on them, so they stay identical in every language.
const (
	notePass           = "pass"
	noteBusy           = "busy"
	noteNonJSON        = "pass(non-json)"
	noteInjectedV1     = "injected:v1"
	noteInjectedNative = "injected:native"
)

// Settings is a live, concurrency-safe view of the mutable proxy settings.
// The portal updates these at runtime, so the proxy reads them per request.
type Settings interface {
	UpstreamURL() *url.URL
	Timeout() time.Duration
	// Policy returns the live global request policy.
	Policy() policy.Policy
}

// Recorder receives one structured entry per proxied request.
type Recorder interface {
	WriteRequest(method, path string, status int, note, client, detail string)
}

// Options configures a Handler.
type Options struct {
	// Upstream is the Ollama base URL used when Settings is nil. Only
	// scheme/host/user are used; any base path on the upstream is dropped.
	Upstream *url.URL
	// Timeout bounds connect and each individual read/write against the
	// upstream. It is not a total request deadline: a stream that keeps
	// producing data may live much longer.
	Timeout time.Duration
	// Settings overrides Upstream/Timeout and is re-read per request,
	// so runtime changes (portal) take effect without a restart.
	Settings Settings
	// MaxConcurrent bounds requests in flight (default 8).
	MaxConcurrent int
	// MaxBodySize bounds bodies read for JSON injection (default 1 MiB).
	MaxBodySize int64
	// AcquireTimeout bounds the wait for a concurrency slot: 0 uses the 2s
	// default, a negative value waits until the client goes away.
	AcquireTimeout time.Duration
	// CaptureRecorder receives one capture per chat request; may be nil.
	CaptureRecorder inspect.Recorder
	// CaptureBodyLimit bounds the in-memory response body kept for Recorder
	// (default 256 KiB). It never changes what is sent to the client.
	CaptureBodyLimit int
	// Recorder receives one structured entry per request; may be nil.
	Recorder Recorder
	// ErrorLog receives net/http and ReverseProxy internals; may be nil.
	ErrorLog io.Writer
}

// staticSettings backs Options.Settings when the caller passes plain values.
type staticSettings struct {
	upstream *url.URL
	timeout  time.Duration
}

func (s staticSettings) UpstreamURL() *url.URL  { return s.upstream }
func (s staticSettings) Timeout() time.Duration { return s.timeout }

func (s staticSettings) Policy() policy.Policy {
	return policy.New(nil)
}

// Handler is the http.Handler that fronts Ollama.
type Handler struct {
	opts      Options
	settings  Settings
	sem       chan struct{}
	transport *http.Transport
	rp        *httputil.ReverseProxy
	now       func() time.Time
}

// New validates opts and builds a Handler with a shared, connection-reusing
// transport.
func New(opts Options) (*Handler, error) {
	upstream := opts.Upstream
	if upstream == nil && opts.Settings != nil {
		upstream = opts.Settings.UpstreamURL()
	}
	if upstream == nil || upstream.Host == "" {
		return nil, errors.New(i18n.T("proxy.errUpstreamEmpty"))
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return nil, fmt.Errorf(i18n.T("proxy.errUpstreamScheme"), upstream.Scheme)
	}
	if opts.Settings == nil {
		opts.Settings = staticSettings{
			upstream: upstream,
			timeout:  opts.Timeout,
		}
	}
	if opts.MaxConcurrent < 1 {
		opts.MaxConcurrent = defaultMaxConcurrent
	}
	if opts.MaxBodySize < 1 {
		opts.MaxBodySize = defaultMaxBodySize
	}
	if opts.CaptureBodyLimit < 1 {
		opts.CaptureBodyLimit = 256 << 10
	}
	if opts.AcquireTimeout == 0 {
		opts.AcquireTimeout = defaultAcquireTimeout
	}

	h := &Handler{
		opts:     opts,
		sem:      make(chan struct{}, opts.MaxConcurrent),
		settings: opts.Settings,
		now:      time.Now,
	}

	dialTimeout := opts.Settings.Timeout()
	if dialTimeout <= 0 {
		dialTimeout = upstreamDialTimeout
	}
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	h.transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &timeoutConn{Conn: conn, timeout: opts.Settings.Timeout}, nil
		},
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: opts.MaxConcurrent,
		MaxConnsPerHost:     opts.MaxConcurrent,
		IdleConnTimeout:     idleConnTimeout,
		// Pure pass-through: never invent Accept-Encoding and never gunzip on
		// behalf of the client.
		DisableCompression: true,
	}

	// Only scheme://[user@]host is forwarded; the request path and query stay
	// exactly as the client sent them. The upstream is re-read per request so
	// portal changes apply without a restart.
	fallbackUpstream := upstream
	rpLogger := log.New(io.Discard, "", 0)
	if opts.ErrorLog != nil {
		rpLogger = log.New(opts.ErrorLog, "proxy: ", 0)
	}
	h.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := h.settings.UpstreamURL()
			if target == nil {
				target = fallbackUpstream
			}
			pr.SetURL(&url.URL{
				Scheme: target.Scheme,
				Host:   target.Host,
				User:   target.User,
			})
			// Deliberately no SetXForwarded: headers go through untouched.
		},
		Transport:     h.transport,
		FlushInterval: 0, // streaming responses flush after every write
		ErrorHandler:  h.handleProxyError,
		ErrorLog:      rpLogger,
	}
	return h, nil
}

// Close releases idle upstream connections.
func (h *Handler) Close() {
	h.transport.CloseIdleConnections()
}

// timeoutConn gives every read/write a deadline, mirroring urllib's
// per-socket-operation timeout without capping the whole response. The
// timeout is read per operation so portal changes apply live.
type timeoutConn struct {
	net.Conn
	timeout func() time.Duration
}

func (c *timeoutConn) Read(b []byte) (int, error) {
	if timeout := c.timeout(); timeout > 0 {
		if err := c.Conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(b)
}

func (c *timeoutConn) Write(b []byte) (int, error) {
	if timeout := c.timeout(); timeout > 0 {
		if err := c.Conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(b)
}

type stateKey struct{}

// reqState carries the injection note and any proxy error from the request
// path to the single log line emitted when the request finishes.
type reqState struct {
	note    string
	err     error
	capture *inspect.Capture
}

func stateFrom(r *http.Request) *reqState {
	if st, ok := r.Context().Value(stateKey{}).(*reqState); ok {
		return st
	}
	return &reqState{note: notePass}
}

func (h *Handler) handleProxyError(w http.ResponseWriter, r *http.Request, err error) {
	if st := stateFrom(r); st != nil {
		st.err = err
	}
	if sw, ok := w.(*statusWriter); ok && sw.status != 0 {
		// Response already started (stream interrupted); nothing sane to add.
		return
	}
	if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
		// Client hung up; that is normal, not an error to answer.
		return
	}
	writeJSONError(w, http.StatusBadGateway, "proxy: "+err.Error())
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	body, err := json.Marshal(map[string]map[string]string{
		"error": {"message": message},
	})
	if err != nil {
		body = []byte(`{"error":{"message":"proxy: internal error"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// statusWriter records the status code for logging and exposes Unwrap so
// http.ResponseController keeps working (streaming depends on Flush).
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}

// Unwrap lets http.NewResponseController find Flush on the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// captureWriter tees the response body into a bounded buffer while writing to
// the client. It must never buffer more than limit bytes.
type captureWriter struct {
	http.ResponseWriter
	capture   *inspect.Capture
	limit     int
	buffer    bytes.Buffer
	truncated bool
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if w.capture != nil && len(p) > 0 && w.capture.TTFT == 0 {
		w.capture.TTFT = time.Since(w.capture.Started)
	}
	if w.capture != nil && w.limit > 0 {
		remaining := w.limit - w.buffer.Len()
		if remaining > 0 {
			chunk := p
			if len(chunk) > remaining {
				chunk = chunk[:remaining]
				w.truncated = true
			}
			_, _ = w.buffer.Write(chunk)
		} else {
			w.truncated = true
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish fills the capture metadata and hands it to the recorder.
func (h *Handler) finish(st *reqState, sw *statusWriter, cw *captureWriter) {
	capture := st.capture
	if capture == nil || h.opts.CaptureRecorder == nil {
		return
	}
	capture.Duration = h.now().Sub(capture.Started)
	if sw != nil {
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		capture.Status = status
		contentType := strings.ToLower(sw.Header().Get("Content-Type"))
		if strings.Contains(contentType, "event-stream") || strings.Contains(contentType, "ndjson") {
			capture.Stream = true
		}
	}
	if capture.Status == 0 {
		capture.Status = http.StatusOK
	}
	if st.err != nil {
		capture.Error = st.err.Error()
	}
	if cw != nil {
		capture.ResponseBody = append([]byte(nil), cw.buffer.Bytes()...)
		capture.ResponseTruncated = cw.truncated
	}
	h.opts.CaptureRecorder.Record(*capture)
}
