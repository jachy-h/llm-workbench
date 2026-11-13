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

	"github.com/jachy-h/ollama-nothink-proxy/internal/i18n"
	"github.com/jachy-h/ollama-nothink-proxy/internal/inject"
)

// ServeHTTP is the whole request pipeline: path check, concurrency slot,
// chat-body rewrite, then ReverseProxy.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := h.now()

	if !validRequestPath(r) {
		h.logLine(r, notePass, http.StatusBadRequest, i18n.T("proxy.errInvalidPath"), 0, h.now().Sub(start))
		writeJSONError(w, http.StatusBadRequest, i18n.T("proxy.errInvalidPath"))
		return
	}

	st := &reqState{note: notePass}
	r = r.WithContext(context.WithValue(r.Context(), stateKey{}, st))

	if !h.acquire(r.Context()) {
		if r.Context().Err() != nil {
			return
		}
		h.logLine(r, noteBusy, http.StatusServiceUnavailable, i18n.T("proxy.errBusy"), 0, h.now().Sub(start))
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusServiceUnavailable, i18n.T("proxy.errBusy"))
		return
	}
	defer func() { <-h.sem }()

	if kind := inject.ChatPathKind(r.URL.Path); kind != inject.KindNone && !bodyless(r.Method) {
		if status, message := h.rewriteChat(r, kind); status != 0 {
			h.logLine(r, st.note, status, message, 0, h.now().Sub(start))
			writeJSONError(w, status, message)
			return
		}
	}

	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		detail := ""
		if st.err != nil {
			detail = st.err.Error()
		}
		h.logLine(r, st.note, status, detail, sw.written, h.now().Sub(start))
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
	params := h.settings.Params()
	switch kind {
	case inject.KindV1:
		out = inject.InjectV1(body, params)
		stateFrom(r).note = noteInjectedV1
	case inject.KindNative:
		var ok bool
		out, ok = inject.InjectNative(body, params)
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
	setRequestBody(r, encoded)
	return 0, ""
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

// logLine records one request with the structured logger and (with -v)
// prints the same line to the console. Client IPs are logged; bodies, headers
// and tokens never are.
func (h *Handler) logLine(r *http.Request, note string, status int, detail string, written int64, dur time.Duration) {
	client := clientIP(r)
	if h.opts.Recorder != nil {
		h.opts.Recorder.WriteRequest(r.Method, r.URL.RequestURI(), status, note, client, detail)
	}
	if !h.opts.Verbose || h.opts.Console == nil {
		return
	}

	var b strings.Builder
	b.WriteString(h.now().Format(time.RFC3339))
	b.WriteByte(' ')
	b.WriteString(client)
	b.WriteByte(' ')
	b.WriteString(r.Method)
	b.WriteByte(' ')
	b.WriteString(r.URL.RequestURI())
	b.WriteString(" -> ")
	b.WriteString(strconv.Itoa(status))
	b.WriteString(" [")
	b.WriteString(note)
	b.WriteString("]")
	if written > 0 {
		b.WriteString(" ")
		b.WriteString(strconv.FormatInt(written, 10))
		b.WriteString("B")
	}
	if dur > 0 {
		b.WriteString(" ")
		b.WriteString(dur.Round(time.Millisecond).String())
	}
	if detail != "" {
		b.WriteString(" ")
		b.WriteString(detail)
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(h.opts.Console, b.String())
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
