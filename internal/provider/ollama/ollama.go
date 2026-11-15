// Package ollama implements the provider interface for Ollama's native API.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jachy-h/llm-workbench/internal/policy"
	"github.com/jachy-h/llm-workbench/internal/provider"
)

const (
	// DefaultID is the provider ID used in config and the workbench API.
	DefaultID = "ollama"
	// maxErrorBody bounds an upstream error body kept for the UI.
	maxErrorBody = 1 << 20
	// maxResponseBody bounds a non-stream response body.
	maxResponseBody = 32 << 20
	// maxRawStream bounds the raw frames retained for inspection.
	maxRawStream = 256 << 10
)

// Provider talks to one Ollama base URL.
type Provider struct {
	base   *url.URL
	client *http.Client
}

// New validates base and builds a provider. client may be nil, in which case
// a no-total-timeout client is used.
func New(base *url.URL, client *http.Client) (*Provider, error) {
	if base == nil || base.Host == "" {
		return nil, errors.New("ollama: base URL is empty")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("ollama: unsupported scheme %q", base.Scheme)
	}
	if client == nil {
		client = provider.Client(30 * time.Second)
	}
	return &Provider{base: base, client: client}, nil
}

// ID implements provider.Provider.
func (p *Provider) ID() string { return DefaultID }

// Name implements provider.Provider.
func (p *Provider) Name() string { return "Ollama" }

// Capabilities implements provider.Provider.
func (p *Provider) Capabilities() provider.Capabilities {
	params := make([]string, 0, len(policy.Specs))
	for _, spec := range policy.Specs {
		params = append(params, spec.Name)
	}
	return provider.Capabilities{
		Streaming:      true,
		Thinking:       provider.ThinkingBool,
		Usage:          true,
		SystemRole:     true,
		ToolCalls:      false,
		SamplingParams: params,
	}
}

// ListModels implements provider.Provider.
func (p *Provider) ListModels(ctx context.Context) ([]provider.Model, error) {
	var payload struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Size    int64  `json:"size"`
			Digest  string `json:"digest"`
			Details struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/api/tags", nil, &payload); err != nil {
		return nil, err
	}
	out := make([]provider.Model, 0, len(payload.Models))
	for _, item := range payload.Models {
		id := item.Model
		if id == "" {
			id = item.Name
		}
		out = append(out, provider.Model{
			ID:            id,
			Name:          item.Name,
			Digest:        item.Digest,
			Family:        item.Details.Family,
			ParameterSize: item.Details.ParameterSize,
			Quantization:  item.Details.QuantizationLevel,
			SizeBytes:     item.Size,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Chat implements provider.Provider.
func (p *Provider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	return p.chat(ctx, req, false, nil)
}

// ChatStream implements provider.Provider.
func (p *Provider) ChatStream(ctx context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) (provider.ChatResponse, error) {
	if emit == nil {
		return p.chat(ctx, req, true, nil)
	}
	return p.chat(ctx, req, true, emit)
}

func (p *Provider) chat(
	ctx context.Context,
	req provider.ChatRequest,
	stream bool,
	emit func(provider.StreamEvent) error,
) (provider.ChatResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return provider.ChatResponse{}, errors.New("ollama: model is required")
	}
	body, err := buildBody(req, stream)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("/api/chat"), bytes.NewReader(body))
	if err != nil {
		return provider.ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson, application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return provider.ChatResponse{}, &provider.HTTPError{Status: resp.StatusCode, Body: raw}
	}
	if stream {
		return decodeStream(resp.Body, emit)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return provider.ChatResponse{}, err
	}
	var frame chatFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return provider.ChatResponse{}, fmt.Errorf("ollama: decode response: %w", err)
	}
	return toResponse(frame, raw), nil
}

func (p *Provider) doJSON(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.endpoint(path), reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &provider.HTTPError{Status: resp.StatusCode, Body: raw}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out)
}

func (p *Provider) endpoint(path string) string {
	base := *p.base
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

type chatFrame struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Message   struct {
		Role      string          `json:"role"`
		Content   string          `json:"content"`
		Thinking  string          `json:"thinking"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	} `json:"message"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	EvalCount          int    `json:"eval_count"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalDuration       int64  `json:"eval_duration"`
}

func toResponse(frame chatFrame, raw []byte) provider.ChatResponse {
	response := provider.ChatResponse{
		Model:        frame.Model,
		Text:         frame.Message.Content,
		Thinking:     frame.Message.Thinking,
		FinishReason: frame.DoneReason,
		Raw:          raw,
		Metrics: provider.Metrics{
			TotalDuration:      time.Duration(frame.TotalDuration),
			LoadDuration:       time.Duration(frame.LoadDuration),
			PromptEvalDuration: time.Duration(frame.PromptEvalDuration),
			EvalDuration:       time.Duration(frame.EvalDuration),
		},
	}
	if frame.PromptEvalCount != 0 || frame.EvalCount != 0 {
		response.Usage = &provider.Usage{
			PromptTokens:     frame.PromptEvalCount,
			CompletionTokens: frame.EvalCount,
			TotalTokens:      frame.PromptEvalCount + frame.EvalCount,
		}
	}
	return response
}

func decodeStream(body io.Reader, emit func(provider.StreamEvent) error) (provider.ChatResponse, error) {
	decoder := json.NewDecoder(body)
	var (
		text     strings.Builder
		thinking strings.Builder
		frames   []json.RawMessage
		rawBytes int
		last     chatFrame
	)
	for {
		var frame chatFrame
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return provider.ChatResponse{}, fmt.Errorf("ollama: decode stream: %w", err)
		}
		last = frame
		if encoded, err := json.Marshal(frame); err == nil && rawBytes+len(encoded) <= maxRawStream {
			frames = append(frames, encoded)
			rawBytes += len(encoded)
		}
		if frame.Message.Content != "" {
			text.WriteString(frame.Message.Content)
			if emit != nil {
				if err := emit(provider.StreamEvent{Type: provider.EventDelta, Text: frame.Message.Content}); err != nil {
					return provider.ChatResponse{}, err
				}
			}
		}
		if frame.Message.Thinking != "" {
			thinking.WriteString(frame.Message.Thinking)
			if emit != nil {
				if err := emit(provider.StreamEvent{Type: provider.EventThinking, Text: frame.Message.Thinking}); err != nil {
					return provider.ChatResponse{}, err
				}
			}
		}
		if frame.Done {
			break
		}
	}

	response := toResponse(last, nil)
	response.Text = text.String()
	response.Thinking = thinking.String()
	if len(frames) > 0 {
		if raw, err := json.Marshal(frames); err == nil {
			response.Raw = raw
		}
	}
	if response.Usage != nil && emit != nil {
		if err := emit(provider.StreamEvent{Type: provider.EventUsage, Usage: response.Usage}); err != nil {
			return provider.ChatResponse{}, err
		}
	}
	if emit != nil {
		if err := emit(provider.StreamEvent{Type: provider.EventDone, Done: true}); err != nil {
			return provider.ChatResponse{}, err
		}
	}
	return response, nil
}

func buildBody(req provider.ChatRequest, stream bool) ([]byte, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   stream,
	}
	if req.Think != nil {
		body["think"] = *req.Think
	}
	options, topLevel := policy.SplitNativeParams(req.Params)
	if len(options) > 0 {
		body["options"] = options
	}
	for key, value := range topLevel {
		if _, present := body[key]; present {
			continue
		}
		body[key] = value
	}
	return encodeBody(body)
}

func encodeBody(body map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
