// Package inspector turns proxy captures into persisted, redacted history.
package inspector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/storage"
)

// Options configures the inspector.
type Options struct {
	Store *storage.Store
	// Enabled switches persistence. A disabled inspector keeps the proxy path
	// free of SQLite work.
	Enabled bool
	// CaptureBodies persists request/response bodies. Metadata is always
	// recorded. The transparent proxy defaults this to false.
	CaptureBodies bool
	// MaxBodyBytes bounds each persisted body (default 256 KiB).
	MaxBodyBytes int
	// QueueSize bounds the in-memory write queue (default 256).
	QueueSize int
	// RetentionDays and MaxRows drive periodic pruning. Zero disables that
	// dimension.
	RetentionDays int
	MaxRows       int
	// Logger receives write errors; nil discards them.
	Logger *log.Logger
}

// Inspector is an asynchronous recorder. Record never blocks the proxy.
type Inspector struct {
	store  *storage.Store
	opts   Options
	queue  chan inspect.Capture
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
	// dropped counts captures discarded because the queue was full.
	dropped atomic.Int64
	// written counts successfully persisted captures.
	written atomic.Int64
}

// New validates options and starts the writer goroutine.
func New(opts Options) (*Inspector, error) {
	if opts.Enabled && opts.Store == nil {
		return nil, errors.New("inspector: store is required when enabled")
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 256 << 10
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.Logger == nil {
		opts.Logger = log.New(io.Discard, "", 0)
	}
	i := &Inspector{
		store: opts.Store,
		opts:  opts,
		queue: make(chan inspect.Capture, opts.QueueSize),
		done:  make(chan struct{}),
	}
	if opts.Enabled {
		go i.run()
	} else {
		close(i.done)
	}
	return i, nil
}

// Record queues a capture for persistence. It never blocks.
func (i *Inspector) Record(capture inspect.Capture) {
	if !i.opts.Enabled {
		return
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.closed {
		return
	}
	select {
	case i.queue <- capture:
	default:
		i.dropped.Add(1)
	}
}

// Dropped reports captures lost to a full queue.
func (i *Inspector) Dropped() int64 { return i.dropped.Load() }

// Written reports captures persisted successfully.
func (i *Inspector) Written() int64 { return i.written.Load() }

// Enabled reports whether persistence is active.
func (i *Inspector) Enabled() bool { return i.opts.Enabled }

// CaptureBodies reports whether bodies are persisted.
func (i *Inspector) CaptureBodies() bool { return i.opts.CaptureBodies }

// Close stops accepting captures, drains the queue and waits for the writer.
func (i *Inspector) Close(ctx context.Context) error {
	i.mu.Lock()
	if !i.closed {
		i.closed = true
		close(i.queue)
	}
	i.mu.Unlock()
	select {
	case <-i.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Maintain prunes old rows according to the configured limits.
func (i *Inspector) Maintain(ctx context.Context) (int64, error) {
	if !i.opts.Enabled || i.store == nil {
		return 0, nil
	}
	var olderThan time.Time
	if i.opts.RetentionDays > 0 {
		olderThan = time.Now().Add(-time.Duration(i.opts.RetentionDays) * 24 * time.Hour)
	}
	return i.store.Prune(ctx, olderThan, i.opts.MaxRows)
}

func (i *Inspector) run() {
	defer close(i.done)
	for capture := range i.queue {
		if err := i.persist(capture); err != nil {
			i.opts.Logger.Printf("inspector: could not store %s: %v", capture.ID, err)
			continue
		}
		i.written.Add(1)
	}
}

func (i *Inspector) persist(capture inspect.Capture) error {
	info := Extract(capture)
	record := storage.Record{
		ID:                capture.ID,
		CreatedAt:         capture.Started,
		Source:            firstNonEmpty(capture.Source, "proxy"),
		Provider:          firstNonEmpty(capture.Provider, info.Provider),
		Model:             info.Model,
		Endpoint:          info.Endpoint,
		Method:            capture.Method,
		Stream:            info.Stream,
		StatusCode:        capture.Status,
		Error:             capture.Error,
		DurationMS:        capture.Duration.Milliseconds(),
		TTFTMS:            capture.TTFT.Milliseconds(),
		Client:            capture.Client,
		Note:              capture.Note,
		SystemPrompt:      info.SystemPrompt,
		ResponseTruncated: capture.ResponseTruncated,
	}
	if capture.Started.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if usage := extractUsage(capture.ResponseBody); usage != nil {
		record.PromptTokens = usage.PromptTokens
		record.CompletionTokens = usage.CompletionTokens
		record.TotalTokens = usage.TotalTokens
	}
	if i.opts.CaptureBodies || capture.CaptureBodies {
		original := redactAndLimit(capture.RequestOriginal, i.opts.MaxBodyBytes)
		forwarded := redactAndLimit(capture.RequestForwarded, i.opts.MaxBodyBytes)
		response := redactAndLimit(capture.ResponseBody, i.opts.MaxBodyBytes)
		record.RequestOriginal = string(original.Data)
		record.RequestForwarded = string(forwarded.Data)
		record.ResponseBody = string(response.Data)
		record.ResponseTruncated = record.ResponseTruncated || original.Truncated || forwarded.Truncated || response.Truncated
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return i.store.Insert(ctx, record)
}

// RequestInfo is metadata extracted from a chat request body.
type RequestInfo struct {
	Provider     string
	Model        string
	Endpoint     string
	Stream       bool
	SystemPrompt string
}

// Extract reads model, stream, endpoint and system prompt from a capture.
func Extract(capture inspect.Capture) RequestInfo {
	info := RequestInfo{Provider: "ollama", Endpoint: capture.Path}
	body := capture.RequestForwarded
	if len(body) == 0 {
		body = capture.RequestOriginal
	}
	if len(body) == 0 {
		return info
	}
	var payload struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return info
	}
	info.Model = payload.Model
	info.Stream = payload.Stream || capture.Stream
	for _, message := range payload.Messages {
		if message.Role == "system" || message.Role == "developer" {
			if info.SystemPrompt == "" {
				info.SystemPrompt = message.Content
			}
			break
		}
	}
	return info
}

// Usage is token accounting extracted from a response.
type Usage struct {
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
}

func extractUsage(body []byte) *Usage {
	if len(body) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var frames []json.RawMessage
		if err := json.Unmarshal(trimmed, &frames); err == nil {
			var last *Usage
			for _, frame := range frames {
				if usage := usageFromObject(frame); usage != nil {
					last = usage
				}
			}
			if last != nil {
				return last
			}
		}
	}
	if usage := usageFromObject(body); usage != nil {
		return usage
	}
	if !bytes.Contains(body, []byte("\n")) {
		return nil
	}
	// SSE / NDJSON: scan bounded frames and keep the last usage found.
	scanner := newFrameScanner(body)
	var last *Usage
	for {
		frame, ok := scanner.next()
		if !ok {
			break
		}
		if usage := usageFromObject(frame); usage != nil {
			last = usage
		}
	}
	return last
}

func usageFromObject(body []byte) *Usage {
	var direct struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &direct); err != nil {
		return nil
	}
	if direct.Usage != nil {
		prompt := direct.Usage.PromptTokens
		completion := direct.Usage.CompletionTokens
		total := direct.Usage.TotalTokens
		return &Usage{&prompt, &completion, &total}
	}
	if direct.PromptEvalCount != 0 || direct.EvalCount != 0 {
		prompt, completion := direct.PromptEvalCount, direct.EvalCount
		total := prompt + completion
		return &Usage{&prompt, &completion, &total}
	}
	return nil
}

// frameScanner yields JSON objects from SSE "data:" lines or NDJSON.
type frameScanner struct {
	lines [][]byte
	index int
}

func newFrameScanner(body []byte) *frameScanner {
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	return &frameScanner{lines: bytes.Split(body, []byte("\n"))}
}

func (s *frameScanner) next() ([]byte, bool) {
	for s.index < len(s.lines) {
		line := bytes.TrimSpace(s.lines[s.index])
		s.index++
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		line = bytes.TrimPrefix(line, []byte("data:"))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		return line, true
	}
	return nil, false
}

type redactedBody struct {
	Data      []byte
	Truncated bool
}

func redactAndLimit(data []byte, limit int) redactedBody {
	if len(data) == 0 {
		return redactedBody{}
	}
	redacted := RedactJSON(data)
	if len(redacted) <= limit {
		return redactedBody{Data: redacted}
	}
	cut := limit
	for cut > 0 && cut < len(redacted) && redacted[cut]&0xC0 == 0x80 {
		cut--
	}
	out := make([]byte, 0, cut+len("\n…[truncated]"))
	out = append(out, redacted[:cut]...)
	out = append(out, []byte("\n…[truncated]")...)
	return redactedBody{Data: out, Truncated: true}
}

var sensitiveKeys = map[string]bool{
	"apikey":        true,
	"api_key":       true,
	"xapikey":       true,
	"x_api_key":     true,
	"authorization": true,
	"token":         true,
	"access_token":  true,
	"accesstoken":   true,
	"refresh_token": true,
	"refreshtoken":  true,
	"password":      true,
	"secret":        true,
	"client_secret": true,
	"cookie":        true,
	"set_cookie":    true,
}

// RedactJSON replaces sensitive object values with "[REDACTED]" while keeping
// unknown fields and number text. Non-JSON input is returned unchanged.
func RedactJSON(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return data
	}
	switch trimmed[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
			return data
		}
		for key, value := range object {
			if sensitiveKeys[normalizeKey(key)] {
				object[key] = json.RawMessage(`"[REDACTED]"`)
				continue
			}
			object[key] = RedactJSON(value)
		}
		encoded, err := encodeNoEscape(object)
		if err != nil {
			return data
		}
		return encoded
	case '[':
		var array []json.RawMessage
		if err := json.Unmarshal(trimmed, &array); err != nil {
			return data
		}
		for index, value := range array {
			array[index] = RedactJSON(value)
		}
		encoded, err := encodeNoEscape(array)
		if err != nil {
			return data
		}
		return encoded
	default:
		return data
	}
}

// RedactURL replaces sensitive query values.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	changed := false
	for key := range query {
		if sensitiveKeys[normalizeKey(key)] {
			query.Set(key, "[REDACTED]")
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	if parsed.User != nil {
		parsed.User = url.User("[REDACTED]")
	}
	return parsed.String()
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	return key
}

func encodeNoEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// String is a debugging helper.
func (u *Usage) String() string {
	if u == nil {
		return "nil"
	}
	return fmt.Sprintf("%v/%v/%v", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}
