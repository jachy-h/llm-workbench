// Package workbench serves the local /_/workbench API and UI.
package workbench

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jachy-h/llm-workbench/internal/config"
	"github.com/jachy-h/llm-workbench/internal/engine"
	"github.com/jachy-h/llm-workbench/internal/experiments"
	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/inspector"
	"github.com/jachy-h/llm-workbench/internal/netutil"
	"github.com/jachy-h/llm-workbench/internal/policy"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/requestid"
	"github.com/jachy-h/llm-workbench/internal/storage"
	webassets "github.com/jachy-h/llm-workbench/web"
)

// Prefix is the reserved workbench route. Everything outside it proxies to
// the upstream.
const Prefix = "/_/workbench"

// Options wires the workbench to the running process.
type Options struct {
	Version   string
	Config    *config.Config
	Store     *storage.Store
	Inspector *inspector.Inspector
	Engine    *engine.Engine
	Runner    *experiments.Runner
}

// Handler serves the workbench routes.
type Handler struct {
	opts      Options
	index     []byte
	fileServe http.Handler
}

// New builds a workbench handler. A nil store is allowed: the UI still loads
// and reports that history is unavailable.
func New(opts Options) *Handler {
	sub, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		panic("workbench: embedded UI is missing: " + err.Error())
	}
	index, _ := fs.ReadFile(sub, "index.html")
	return &Handler{
		opts:      opts,
		index:     index,
		fileServe: http.StripPrefix(Prefix+"/", http.FileServer(http.FS(sub))),
	}
}

// Handle serves the request and reports whether it belonged to the workbench.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != Prefix && !strings.HasPrefix(r.URL.Path, Prefix+"/") {
		return false
	}
	if !netutil.Guard(r) {
		writeJSONError(w, http.StatusForbidden, "workbench: loopback access only")
		return true
	}
	if r.URL.Path == Prefix {
		http.Redirect(w, r, Prefix+"/", http.StatusTemporaryRedirect)
		return true
	}
	route := strings.TrimPrefix(r.URL.Path, Prefix)
	if route == "" || route == "/" {
		h.serveIndex(w, r)
		return true
	}
	if strings.HasPrefix(route, "/api/") {
		h.serveAPI(w, r, route)
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		return true
	}
	if strings.HasPrefix(route, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.fileServe.ServeHTTP(w, r)
		return true
	}
	// SPA fallback: unknown non-API paths render the app shell.
	h.serveIndex(w, r)
	return true
}

// ServeIndex serves the embedded React shell for routes outside Prefix. The
// proxy settings page uses the same bundle, so /_/llm-proxy is also handled by
// this app while its API stays in internal/portal.
func (h *Handler) ServeIndex(w http.ResponseWriter, r *http.Request) {
	if !netutil.Guard(r) {
		writeJSONError(w, http.StatusForbidden, "workbench: loopback access only")
		return
	}
	h.serveIndex(w, r)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(h.index)
	}
}

func (h *Handler) serveAPI(w http.ResponseWriter, r *http.Request, route string) {
	parts := strings.Split(strings.Trim(route, "/"), "/")
	// parts[0] = "api"
	if len(parts) < 2 {
		writeJSONError(w, http.StatusNotFound, "workbench: unknown route")
		return
	}
	switch parts[1] {
	case "requests", "health", "stats", "providers", "chat", "prompts", "experiments", "runs":
	default:
		writeJSONError(w, http.StatusNotFound, "workbench: unknown route")
		return
	}
	if h.opts.Store == nil && (parts[1] == "requests" || parts[1] == "stats" || parts[1] == "prompts" || parts[1] == "experiments" || parts[1] == "runs") {
		writeJSONError(w, http.StatusServiceUnavailable, "workbench: history database is unavailable")
		return
	}
	switch {
	case parts[1] == "health" && len(parts) == 2 && r.Method == http.MethodGet:
		h.health(w)
	case parts[1] == "stats" && len(parts) == 2 && r.Method == http.MethodGet:
		h.stats(w)
	case parts[1] == "providers" && len(parts) == 2 && r.Method == http.MethodGet:
		h.listProviders(w)
	case parts[1] == "providers" && len(parts) == 4 && parts[3] == "models" && r.Method == http.MethodGet:
		h.listModels(w, r, parts[2])
	case parts[1] == "chat" && len(parts) == 2 && r.Method == http.MethodPost:
		h.chat(w, r)
	case parts[1] == "prompts" && len(parts) == 2:
		switch r.Method {
		case http.MethodGet:
			h.listPrompts(w, r)
		case http.MethodPost:
			h.createPrompt(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		}
	case parts[1] == "prompts" && len(parts) == 3:
		switch r.Method {
		case http.MethodGet:
			h.getPrompt(w, r, parts[2])
		case http.MethodPut:
			h.updatePrompt(w, r, parts[2])
		case http.MethodDelete:
			h.deletePrompt(w, r, parts[2])
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		}
	case parts[1] == "experiments" && len(parts) == 2:
		switch r.Method {
		case http.MethodGet:
			h.listExperiments(w, r)
		case http.MethodPost:
			h.createExperiment(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		}
	case parts[1] == "experiments" && len(parts) == 3:
		switch r.Method {
		case http.MethodGet:
			h.getExperiment(w, r, parts[2])
		case http.MethodPut:
			h.updateExperiment(w, r, parts[2])
		case http.MethodDelete:
			h.deleteExperiment(w, r, parts[2])
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		}
	case parts[1] == "experiments" && len(parts) == 4 && parts[3] == "run" && r.Method == http.MethodPost:
		h.runExperiment(w, r, parts[2])
	case parts[1] == "experiments" && len(parts) == 4 && parts[3] == "cancel" && r.Method == http.MethodPost:
		h.cancelExperiment(w, r, parts[2])
	case parts[1] == "experiments" && len(parts) == 4 && parts[3] == "runs" && r.Method == http.MethodGet:
		h.listRuns(w, r, parts[2])
	case parts[1] == "experiments" && len(parts) == 4 && parts[3] == "export" && r.Method == http.MethodGet:
		h.exportExperiment(w, r, parts[2])
	case parts[1] == "runs" && len(parts) == 3:
		switch r.Method {
		case http.MethodGet:
			h.getRun(w, r, parts[2])
		case http.MethodPatch:
			h.rateRun(w, r, parts[2])
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		}
	case len(parts) == 2 && parts[1] == "requests":
		switch r.Method {
		case http.MethodGet:
			h.listRequests(w, r)
		case http.MethodDelete:
			h.deleteFiltered(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
		}
	case len(parts) == 3 && parts[1] == "requests" && parts[2] == "export" && r.Method == http.MethodGet:
		h.export(w, r, "")
	case len(parts) == 3 && parts[1] == "requests":
		h.requestByID(w, r, parts[2], false)
	case len(parts) == 4 && parts[1] == "requests" && parts[3] == "export" && r.Method == http.MethodGet:
		h.requestByID(w, r, parts[2], true)
	default:
		writeJSONError(w, http.StatusNotFound, "workbench: unknown route")
	}
}

// providerInfo is the provider list shape used by the playground.
type providerInfo struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Capabilities provider.Capabilities `json:"capabilities"`
	Default      bool                  `json:"default"`
}

func (h *Handler) listProviders(w http.ResponseWriter) {
	if h.opts.Engine == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workbench: engine is unavailable")
		return
	}
	providers := h.opts.Engine.Providers()
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]providerInfo, 0, len(ids))
	defaultID := h.opts.Engine.DefaultProvider()
	for _, id := range ids {
		p := providers[id]
		items = append(items, providerInfo{
			ID:           p.ID(),
			Name:         p.Name(),
			Capabilities: p.Capabilities(),
			Default:      id == defaultID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "default": defaultID})
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request, id string) {
	if h.opts.Engine == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workbench: engine is unavailable")
		return
	}
	p, ok := h.opts.Engine.Providers()[id]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "workbench: unknown provider")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	models, err := p.ListModels(ctx)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": models})
}

type chatBody struct {
	Provider string                     `json:"provider"`
	Model    string                     `json:"model"`
	System   string                     `json:"system"`
	Messages []provider.Message         `json:"messages"`
	Params   map[string]json.RawMessage `json:"params"`
	Think    *bool                      `json:"think"`
	Stream   bool                       `json:"stream"`
}

type chatResponse struct {
	RequestID    string          `json:"request_id"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Text         string          `json:"text"`
	Thinking     string          `json:"thinking"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Usage        *provider.Usage `json:"usage,omitempty"`
	DurationMS   int64           `json:"duration_ms"`
	TTFTMS       int64           `json:"ttft_ms"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	if h.opts.Engine == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workbench: engine is unavailable")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: could not read body")
		return
	}
	var body chatBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: body must be a JSON object")
		return
	}
	if body.Model == "" || len(body.Messages) == 0 {
		writeJSONError(w, http.StatusBadRequest, "workbench: model and messages are required")
		return
	}
	request := engine.Request{
		Provider:     body.Provider,
		Model:        body.Model,
		Messages:     body.Messages,
		Params:       body.Params,
		Thinking:     thinkingMode(body.Think),
		Stream:       body.Stream,
		RequestID:    requestid.New(),
		SystemPrompt: policy.SystemPrompt{},
	}
	if strings.TrimSpace(body.System) != "" {
		request.SystemPrompt = policy.SystemPrompt{
			Mode: policy.SystemPromptOverride,
			Text: body.System,
		}
	}
	if request.Provider == "" {
		request.Provider = h.opts.Engine.DefaultProvider()
	}
	if body.Stream {
		h.chatStream(w, r, request, raw)
		return
	}
	h.chatOnce(w, r, request, raw)
}

func thinkingMode(think *bool) policy.ThinkingMode {
	if think == nil {
		return policy.ThinkingPassthrough
	}
	if *think {
		return policy.ThinkingOn
	}
	return policy.ThinkingOff
}

func (h *Handler) chatOnce(w http.ResponseWriter, r *http.Request, request engine.Request, original []byte) {
	started := time.Now()
	result, err := h.opts.Engine.Chat(r.Context(), request)
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	h.captureChat(r, original, request, started, result, err, status, false)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chatResponse{
		RequestID:    result.RequestID,
		Provider:     result.Provider,
		Model:        result.Response.Model,
		Text:         result.Response.Text,
		Thinking:     result.Response.Thinking,
		FinishReason: result.Response.FinishReason,
		Usage:        result.Response.Usage,
		DurationMS:   result.Duration.Milliseconds(),
		TTFTMS:       result.TTFT.Milliseconds(),
		Raw:          result.Response.Raw,
	})
}

func (h *Handler) chatStream(w http.ResponseWriter, r *http.Request, request engine.Request, original []byte) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "workbench: streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = writeSSE(w, flusher, "meta", map[string]any{
		"request_id": request.RequestID,
		"provider":   request.Provider,
		"model":      request.Model,
		"started_at": time.Now().UTC().Format(time.RFC3339Nano),
	})

	started := time.Now()
	usageSent := false
	emit := func(event provider.StreamEvent) error {
		switch event.Type {
		case provider.EventDelta:
			return writeSSE(w, flusher, "delta", map[string]string{"text": event.Text})
		case provider.EventThinking:
			return writeSSE(w, flusher, "thinking", map[string]string{"text": event.Text})
		case provider.EventUsage:
			if event.Usage != nil {
				usageSent = true
				return writeSSE(w, flusher, "usage", event.Usage)
			}
		}
		return nil
	}
	result, err := h.opts.Engine.ChatStream(r.Context(), request, emit)
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
		if r.Context().Err() != nil {
			status = 499
		}
	}
	h.captureChat(r, original, request, started, result, err, status, true)
	if err != nil {
		if r.Context().Err() == nil {
			_ = writeSSE(w, flusher, "error", map[string]string{"message": err.Error()})
		}
		return
	}
	if !usageSent && result.Response.Usage != nil {
		_ = writeSSE(w, flusher, "usage", result.Response.Usage)
	}
	_ = writeSSE(w, flusher, "done", map[string]any{
		"request_id":    result.RequestID,
		"finish_reason": result.Response.FinishReason,
		"duration_ms":   result.Duration.Milliseconds(),
		"ttft_ms":       result.TTFT.Milliseconds(),
		"usage":         result.Response.Usage,
		"raw":           result.Response.Raw,
	})
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := marshalNoEscape(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func marshalNoEscape(value any) ([]byte, error) {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(buf.String(), "\n")), nil
}

func (h *Handler) captureChat(
	r *http.Request,
	original []byte,
	request engine.Request,
	started time.Time,
	result engine.Result,
	err error,
	status int,
	stream bool,
) {
	if h.opts.Inspector == nil || !h.opts.Inspector.Enabled() {
		return
	}
	providerID := result.Provider
	if providerID == "" {
		providerID = request.Provider
	}
	if providerID == "" {
		providerID = "ollama"
	}
	forwarded, _ := marshalNoEscape(result.Prepared)
	capture := inspect.Capture{
		ID:               request.RequestID,
		Started:          started,
		Source:           "playground",
		Provider:         providerID,
		Method:           http.MethodPost,
		Path:             "/api/chat",
		Client:           r.RemoteAddr,
		Note:             "playground",
		Stream:           stream,
		Status:           status,
		Duration:         result.Duration,
		TTFT:             result.TTFT,
		RequestOriginal:  original,
		RequestForwarded: forwarded,
		ResponseBody:     result.Response.Raw,
		CaptureBodies:    true,
	}
	if capture.Duration == 0 {
		capture.Duration = time.Since(started)
	}
	if err != nil {
		capture.Error = err.Error()
	}
	h.opts.Inspector.Record(capture)
}

type promptBody struct {
	Name     string          `json:"name"`
	System   string          `json:"system"`
	Messages json.RawMessage `json:"messages"`
	Params   json.RawMessage `json:"params"`
}

func (h *Handler) listPrompts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := h.opts.Store.ListPrompts(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getPrompt(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	prompt, err := h.opts.Store.GetPrompt(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "workbench: prompt not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, prompt)
}

func (h *Handler) createPrompt(w http.ResponseWriter, r *http.Request) {
	body, ok := decodePrompt(w, r)
	if !ok {
		return
	}
	prompt := storage.Prompt{
		ID:       requestid.NewWithPrefix("prm_"),
		Name:     body.Name,
		System:   body.System,
		Messages: body.Messages,
		Params:   body.Params,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.opts.Store.CreatePrompt(ctx, prompt); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := h.opts.Store.GetPrompt(ctx, prompt.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) updatePrompt(w http.ResponseWriter, r *http.Request, id string) {
	body, ok := decodePrompt(w, r)
	if !ok {
		return
	}
	prompt := storage.Prompt{
		ID:       id,
		Name:     body.Name,
		System:   body.System,
		Messages: body.Messages,
		Params:   body.Params,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.opts.Store.UpdatePrompt(ctx, prompt); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "workbench: prompt not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.opts.Store.GetPrompt(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) deletePrompt(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	deleted, err := h.opts.Store.DeletePrompt(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeJSONError(w, http.StatusNotFound, "workbench: prompt not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": 1})
}

func decodePrompt(w http.ResponseWriter, r *http.Request) (promptBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: could not read body")
		return promptBody{}, false
	}
	var body promptBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: body must be a JSON object")
		return promptBody{}, false
	}
	if strings.TrimSpace(body.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "workbench: prompt name is required")
		return promptBody{}, false
	}
	if len(body.Messages) == 0 {
		body.Messages = json.RawMessage("[]")
	}
	if len(body.Params) == 0 {
		body.Params = json.RawMessage("{}")
	}
	var messages []provider.Message
	if err := json.Unmarshal(body.Messages, &messages); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: messages must be an array")
		return promptBody{}, false
	}
	if messages == nil {
		body.Messages = json.RawMessage("[]")
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(body.Params, &params); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: params must be an object")
		return promptBody{}, false
	}
	if params == nil {
		body.Params = json.RawMessage("{}")
	}
	return body, true
}

type experimentBody struct {
	Name     string        `json:"name"`
	Notes    string        `json:"notes"`
	Variants []variantBody `json:"variants"`
	Cases    []caseBody    `json:"cases"`
}

type variantBody struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	System   string          `json:"system"`
	Params   json.RawMessage `json:"params"`
}

type caseBody struct {
	Name     string          `json:"name"`
	Messages json.RawMessage `json:"messages"`
}

func (h *Handler) listExperiments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := h.opts.Store.ListExperiments(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getExperiment(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	experiment, err := h.opts.Store.GetExperiment(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "workbench: experiment not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, experiment)
}

func (h *Handler) createExperiment(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeExperiment(w, r)
	if !ok {
		return
	}
	experiment := storage.Experiment{ID: requestid.NewWithPrefix("exp_"), Name: body.Name, Notes: body.Notes}
	for index, variant := range body.Variants {
		experiment.Variants = append(experiment.Variants, storage.Variant{
			ID:        requestid.NewWithPrefix("var_"),
			Provider:  variant.Provider,
			Model:     variant.Model,
			System:    variant.System,
			Params:    variant.Params,
			SortOrder: index,
		})
	}
	for index, item := range body.Cases {
		experiment.Cases = append(experiment.Cases, storage.Case{
			ID:        requestid.NewWithPrefix("cas_"),
			Name:      item.Name,
			Messages:  item.Messages,
			SortOrder: index,
		})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.opts.Store.CreateExperiment(ctx, experiment); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := h.opts.Store.GetExperiment(ctx, experiment.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateExperiment(w http.ResponseWriter, r *http.Request, id string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: could not read body")
		return
	}
	var body struct {
		Name  string `json:"name"`
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: body must be a JSON object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.opts.Store.UpdateExperiment(ctx, id, body.Name, body.Notes); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "workbench: experiment not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	experiment, err := h.opts.Store.GetExperiment(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, experiment)
}

func (h *Handler) deleteExperiment(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	deleted, err := h.opts.Store.DeleteExperiment(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeJSONError(w, http.StatusNotFound, "workbench: experiment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": 1})
}

func (h *Handler) runExperiment(w http.ResponseWriter, r *http.Request, id string) {
	if h.opts.Runner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workbench: experiment runner is unavailable")
		return
	}
	result, err := h.opts.Runner.Start(id)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) cancelExperiment(w http.ResponseWriter, r *http.Request, id string) {
	if h.opts.Runner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workbench: experiment runner is unavailable")
		return
	}
	if !h.opts.Runner.Cancel(id) {
		writeJSONError(w, http.StatusNotFound, "workbench: no active batch")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	runs, err := h.opts.Store.ListRuns(ctx, id, strings.TrimSpace(r.URL.Query().Get("batch")))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  runs,
		"active": h.opts.Runner != nil && h.opts.Runner.Active(id),
	})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	run, err := h.opts.Store.GetRun(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "workbench: run not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) rateRun(w http.ResponseWriter, r *http.Request, id string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: could not read body")
		return
	}
	var body struct {
		Rating *int   `json:"rating"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: body must be a JSON object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.opts.Store.SetRunRating(ctx, id, body.Rating, body.Note); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "workbench: run not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	run, err := h.opts.Store.GetRun(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) exportExperiment(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	experiment, err := h.opts.Store.GetExperiment(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "workbench: experiment not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs, err := h.opts.Store.ListRuns(ctx, id, "")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="experiment.csv"`)
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"experiment", "variant_id", "provider", "model", "case_id", "case_name",
			"run_id", "status", "output", "thinking", "prompt_tokens", "completion_tokens",
			"duration_ms", "ttft_ms", "rating", "rating_note", "error"})
		variants := map[string]storage.Variant{}
		for _, variant := range experiment.Variants {
			variants[variant.ID] = variant
		}
		cases := map[string]storage.Case{}
		for _, item := range experiment.Cases {
			cases[item.ID] = item
		}
		for _, run := range runs {
			variant := variants[run.VariantID]
			item := cases[run.CaseID]
			_ = writer.Write([]string{
				experiment.Name, variant.ID, variant.Provider, variant.Model, item.ID, item.Name,
				run.ID, run.Status, run.Output, run.Thinking,
				optionalInt(run.PromptTokens), optionalInt(run.CompletionTokens),
				strconv.FormatInt(run.DurationMS, 10), strconv.FormatInt(run.TTFTMS, 10),
				optionalInt(run.Rating), run.RatingNote, run.Error,
			})
		}
		writer.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="experiment.json"`)
	writeJSONBody(w, http.StatusOK, map[string]any{"experiment": experiment, "runs": runs})
}

func decodeExperiment(w http.ResponseWriter, r *http.Request) (experimentBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: could not read body")
		return experimentBody{}, false
	}
	var body experimentBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "workbench: body must be a JSON object")
		return experimentBody{}, false
	}
	if strings.TrimSpace(body.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "workbench: experiment name is required")
		return experimentBody{}, false
	}
	if len(body.Variants) == 0 || len(body.Cases) == 0 {
		writeJSONError(w, http.StatusBadRequest, "workbench: at least one variant and one case are required")
		return experimentBody{}, false
	}
	for index := range body.Variants {
		variant := &body.Variants[index]
		if variant.Provider == "" || variant.Model == "" {
			writeJSONError(w, http.StatusBadRequest, "workbench: variant provider and model are required")
			return experimentBody{}, false
		}
		if len(variant.Params) == 0 {
			variant.Params = json.RawMessage("{}")
		}
		var params map[string]json.RawMessage
		if err := json.Unmarshal(variant.Params, &params); err != nil || params == nil {
			writeJSONError(w, http.StatusBadRequest, "workbench: variant params must be an object")
			return experimentBody{}, false
		}
	}
	for index := range body.Cases {
		item := &body.Cases[index]
		if len(item.Messages) == 0 {
			writeJSONError(w, http.StatusBadRequest, "workbench: case messages are required")
			return experimentBody{}, false
		}
		var messages []provider.Message
		if err := json.Unmarshal(item.Messages, &messages); err != nil || len(messages) == 0 {
			writeJSONError(w, http.StatusBadRequest, "workbench: case messages must be a non-empty array")
			return experimentBody{}, false
		}
	}
	return body, true
}

func writeJSONBody(w http.ResponseWriter, status int, payload any) {
	data, err := marshalNoEscape(payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "workbench: could not encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func (h *Handler) health(w http.ResponseWriter) {
	payload := map[string]any{"version": h.opts.Version}
	inspectorState := map[string]any{"enabled": false, "capture_bodies": false, "written": 0, "dropped": 0}
	if h.opts.Inspector != nil {
		inspectorState["enabled"] = h.opts.Inspector.Enabled()
		inspectorState["capture_bodies"] = h.opts.Inspector.CaptureBodies()
		inspectorState["written"] = h.opts.Inspector.Written()
		inspectorState["dropped"] = h.opts.Inspector.Dropped()
	}
	payload["inspector"] = inspectorState
	if h.opts.Store == nil {
		payload["storage"] = map[string]any{"count": 0, "size_bytes": 0, "path": ""}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stats, err := h.opts.Store.Stats(ctx)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		payload["storage"] = stats
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) stats(w http.ResponseWriter) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stats, err := h.opts.Store.Stats(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, total, err := h.opts.Store.List(ctx, filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func (h *Handler) requestByID(w http.ResponseWriter, r *http.Request, id string, export bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "workbench: request id is empty")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch {
	case export && r.Method == http.MethodGet:
		record, err := h.opts.Store.Get(ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "workbench: request not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeExport(w, r, "json", []storage.Record{record})
	case r.Method == http.MethodGet:
		record, err := h.opts.Store.Get(ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "workbench: request not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, detailFrom(record))
	case r.Method == http.MethodDelete:
		deleted, err := h.opts.Store.Delete(ctx, id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !deleted {
			writeJSONError(w, http.StatusNotFound, "workbench: request not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": 1})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "workbench: method not allowed")
	}
}

func (h *Handler) deleteFiltered(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	all := r.URL.Query().Get("all") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	deleted, err := h.opts.Store.DeleteFiltered(ctx, filter, all)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request, id string) {
	if id != "" {
		h.requestByID(w, r, id, true)
		return
	}
	filter, err := parseFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter.Limit == 0 || filter.Limit > 5000 {
		filter.Limit = 1000
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	records, err := h.opts.Store.Export(ctx, filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "json"
	}
	writeExport(w, r, format, records)
}

func writeExport(w http.ResponseWriter, r *http.Request, format string, records []storage.Record) {
	switch format {
	case "jsonl":
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="requests.jsonl"`)
		encoder := json.NewEncoder(w)
		for _, record := range records {
			if err := encoder.Encode(detailFrom(record)); err != nil {
				return
			}
		}
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="requests.csv"`)
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"id", "created_at", "source", "provider", "model", "endpoint", "method",
			"stream", "status_code", "duration_ms", "ttft_ms", "prompt_tokens", "completion_tokens", "total_tokens",
			"client", "note", "system_prompt", "error", "request_original", "request_forwarded", "response_body", "response_truncated"})
		for _, record := range records {
			_ = writer.Write([]string{
				record.ID, record.CreatedAt.UTC().Format(time.RFC3339Nano), record.Source, record.Provider,
				record.Model, record.Endpoint, record.Method, strconv.FormatBool(record.Stream),
				strconv.Itoa(record.StatusCode), strconv.FormatInt(record.DurationMS, 10), strconv.FormatInt(record.TTFTMS, 10),
				optionalInt(record.PromptTokens), optionalInt(record.CompletionTokens), optionalInt(record.TotalTokens),
				record.Client, record.Note, record.SystemPrompt, record.Error,
				record.RequestOriginal, record.RequestForwarded, record.ResponseBody, strconv.FormatBool(record.ResponseTruncated),
			})
		}
		writer.Flush()
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="requests.json"`)
		out := make([]requestDetail, 0, len(records))
		for _, record := range records {
			out = append(out, detailFrom(record))
		}
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(out)
	default:
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("workbench: unknown export format %q", format))
	}
}

func parseFilter(r *http.Request) (storage.Filter, error) {
	query := r.URL.Query()
	filter := storage.Filter{
		Limit:    queryInt(query.Get("limit"), 100),
		Offset:   queryInt(query.Get("offset"), 0),
		Provider: strings.TrimSpace(query.Get("provider")),
		Model:    strings.TrimSpace(query.Get("model")),
		Source:   strings.TrimSpace(query.Get("source")),
		Query:    strings.TrimSpace(query.Get("q")),
	}
	if filter.Limit < 0 || filter.Limit > 500 {
		return filter, errors.New("workbench: limit must be between 1 and 500")
	}
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		return filter, errors.New("workbench: offset must not be negative")
	}
	if raw := strings.TrimSpace(query.Get("status")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return filter, errors.New("workbench: status must be a number")
		}
		filter.Status = &value
	}
	var err error
	if filter.Since, err = parseTime(query.Get("since")); err != nil {
		return filter, err
	}
	if filter.Until, err = parseTime(query.Get("until")); err != nil {
		return filter, err
	}
	return filter, nil
}

func parseTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errors.New("workbench: time filters must be RFC3339")
	}
	return &value, nil
}

type requestDetail struct {
	storage.Summary
	RequestOriginal  string `json:"request_original,omitempty"`
	RequestForwarded string `json:"request_forwarded,omitempty"`
	ResponseBody     string `json:"response_body,omitempty"`
}

func detailFrom(record storage.Record) requestDetail {
	return requestDetail{
		Summary: storage.Summary{
			ID:                record.ID,
			CreatedAt:         record.CreatedAt,
			Source:            record.Source,
			Provider:          record.Provider,
			Model:             record.Model,
			Endpoint:          record.Endpoint,
			Method:            record.Method,
			Stream:            record.Stream,
			StatusCode:        record.StatusCode,
			Error:             record.Error,
			DurationMS:        record.DurationMS,
			TTFTMS:            record.TTFTMS,
			PromptTokens:      record.PromptTokens,
			CompletionTokens:  record.CompletionTokens,
			TotalTokens:       record.TotalTokens,
			Client:            record.Client,
			Note:              record.Note,
			SystemPrompt:      record.SystemPrompt,
			ResponseTruncated: record.ResponseTruncated,
		},
		RequestOriginal:  record.RequestOriginal,
		RequestForwarded: record.RequestForwarded,
		ResponseBody:     record.ResponseBody,
	}
}

func queryInt(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
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
