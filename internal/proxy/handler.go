package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jachy-h/llm-workbench/internal/i18n"
	"github.com/jachy-h/llm-workbench/internal/inject"
	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/policy"
	"github.com/jachy-h/llm-workbench/internal/requestid"
)

// ServeHTTP is the whole request pipeline: path check, concurrency slot,
// chat-body rewrite, then ReverseProxy.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := h.now()

	if !validRequestPath(r) {
		h.logLine(r, notePass, http.StatusBadRequest, i18n.T("proxy.errInvalidPath"))
		writeJSONError(w, http.StatusBadRequest, i18n.T("proxy.errInvalidPath"))
		return
	}

	st := &reqState{note: notePass}
	r = r.WithContext(context.WithValue(r.Context(), stateKey{}, st))

	if !h.acquire(r.Context()) {
		if r.Context().Err() != nil {
			return
		}
		h.logLine(r, noteBusy, http.StatusServiceUnavailable, i18n.T("proxy.errBusy"))
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusServiceUnavailable, i18n.T("proxy.errBusy"))
		return
	}
	defer func() { <-h.sem }()

	kind := inject.ChatPathKind(r.URL.Path)
	if h.opts.CaptureRecorder != nil && kind != inject.KindNone && !bodyless(r.Method) {
		st.capture = &inspect.Capture{
			ID:       requestid.New(),
			Started:  start,
			Source:   "proxy",
			Provider: "ollama",
			Method:   r.Method,
			Path:     r.URL.Path,
			Client:   clientIP(r),
		}
	}
	var sw *statusWriter
	var cw *captureWriter
	defer func() { h.finish(st, sw, cw) }()

	if kind != inject.KindNone && !bodyless(r.Method) {
		if status, message := h.rewriteChat(r, kind); status != 0 {
			if st.capture != nil {
				st.capture.Status = status
				st.capture.Error = message
			}
			h.logLine(r, st.note, status, message)
			writeJSONError(w, status, message)
			return
		}
	}

	rw := w
	if st.capture != nil {
		cw = &captureWriter{ResponseWriter: w, capture: st.capture, limit: h.opts.CaptureBodyLimit}
		rw = cw
	}
	sw = &statusWriter{ResponseWriter: rw}
	defer func() {
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		detail := ""
		if st.err != nil {
			detail = st.err.Error()
		}
		h.logLine(r, st.note, status, detail)
	}()

	h.rp.ServeHTTP(sw, r)
}

func bodyless(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func validRequestPath(r *http.Request) bool {
	if r.URL == nil || r.URL.Host != "" {
		return false
	}
	path := r.URL.Path
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//")
}

// acquire takes a concurrency slot. It returns false when the slot wait timed
// out or the client went away; callers must not respond in the latter case.
func (h *Handler) acquire(ctx context.Context) bool {
	if h.opts.AcquireTimeout < 0 {
		select {
		case h.sem <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	timer := time.NewTimer(h.opts.AcquireTimeout)
	defer timer.Stop()
	select {
	case h.sem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// rewriteChat reads at most MaxBodySize bytes and rewrites the body in place.
// A non-zero status is an error the caller must send to the client.
func (h *Handler) rewriteChat(r *http.Request, kind inject.Kind) (status int, message string) {
	pol, status, message := h.requestPolicy(r)
	if status != 0 {
		return status, message
	}
	if r.Body == nil {
		return 0, ""
	}
	limited := io.LimitReader(r.Body, h.opts.MaxBodySize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return http.StatusBadRequest, fmt.Sprintf(i18n.T("proxy.errReadBody"), err)
	}
	if int64(len(data)) > h.opts.MaxBodySize {
		return http.StatusRequestEntityTooLarge,
			fmt.Sprintf(i18n.T("proxy.errBodyTooLarge"), h.opts.MaxBodySize)
	}
	if capture := stateFrom(r).capture; capture != nil {
		capture.RequestOriginal = append([]byte(nil), data...)
		capture.RequestForwarded = append([]byte(nil), data...)
	}

	// Restore the body first: every early return below is a pass-through.
	setRequestBody(r, data)

	if len(bytes.TrimSpace(data)) == 0 {
		return 0, ""
	}
	body, ok := inject.DecodeObject(data)
	if !ok {
		stateFrom(r).note = noteNonJSON
		return 0, ""
	}

	var out map[string]json.RawMessage
	switch kind {
	case inject.KindV1:
		out = inject.ApplyV1(body, pol)
		stateFrom(r).note = noteInjectedV1
	case inject.KindNative:
		var ok bool
		out, ok = inject.ApplyNative(body, pol)
		if !ok {
			stateFrom(r).note = noteNonJSON
			return 0, ""
		}
		stateFrom(r).note = noteInjectedNative
	}

	encoded, err := inject.Encode(out)
	if err != nil {
		stateFrom(r).note = noteNonJSON
		return 0, ""
	}
	if capture := stateFrom(r).capture; capture != nil {
		capture.RequestForwarded = append([]byte(nil), encoded...)
	}
	setRequestBody(r, encoded)
	return 0, ""
}

// requestPolicy resolves the global request policy. The removed
// X-Workbench-* control headers are rejected instead of forwarded upstream.
func (h *Handler) requestPolicy(r *http.Request) (pol policy.Policy, status int, message string) {
	if len(r.Header.Values("X-Workbench-Profile")) > 0 {
		return policy.Policy{}, http.StatusBadRequest, i18n.T("proxy.errProfileHeader")
	}
	if len(r.Header.Values("X-Workbench-Thinking")) > 0 {
		return policy.Policy{}, http.StatusBadRequest, i18n.T("proxy.errThinkingHeader")
	}
	return h.settings.Policy(), 0, ""
}

// setRequestBody replaces the body and keeps ContentLength / transfer
// encoding consistent so net/http recomputes framing itself.
func setRequestBody(r *http.Request, data []byte) {
	if len(data) == 0 {
		r.Body = http.NoBody
	} else {
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	r.ContentLength = int64(len(data))
	r.TransferEncoding = nil
	if r.Header != nil {
		r.Header.Del("Content-Length")
		r.Header.Del("Transfer-Encoding")
		if len(data) > 0 {
			r.Header.Set("Content-Length", strconv.Itoa(len(data)))
		}
	}
}

// logLine records one request with the structured logger. Client IPs are
// logged; bodies, headers and tokens never are.
func (h *Handler) logLine(r *http.Request, note string, status int, detail string) {
	if h.opts.Recorder == nil {
		return
	}
	h.opts.Recorder.WriteRequest(r.Method, r.URL.RequestURI(), status, note, clientIP(r), detail)
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
