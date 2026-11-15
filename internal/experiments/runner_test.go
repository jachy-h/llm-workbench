package experiments

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/engine"
	"github.com/jachy-h/llm-workbench/internal/inspector"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/storage"
)

type fakeProvider struct {
	delay int
}

func (f *fakeProvider) ID() string   { return "fake" }
func (f *fakeProvider) Name() string { return "Fake" }
func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Streaming: false, Thinking: provider.ThinkingBool, Usage: true}
}
func (f *fakeProvider) ListModels(context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "m", Name: "m"}}, nil
}
func (f *fakeProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if f.delay > 0 {
		select {
		case <-time.After(time.Duration(f.delay) * time.Millisecond):
		case <-ctx.Done():
			return provider.ChatResponse{}, ctx.Err()
		}
	}
	return provider.ChatResponse{
		Model: req.Model, Text: "ok " + req.Model,
		Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Raw:   json.RawMessage(`{"ok":true}`),
	}, nil
}
func (f *fakeProvider) ChatStream(context.Context, provider.ChatRequest, func(provider.StreamEvent) error) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, nil
}

func setup(t *testing.T, delay int) (*Runner, *storage.Store, string) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	providerImpl := &fakeProvider{delay: delay}
	eng, err := engine.New(map[string]provider.Provider{"fake": providerImpl}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	insp, err := inspector.New(inspector.Options{Store: store, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	experiment := storage.Experiment{
		ID:   "exp",
		Name: "test",
		Variants: []storage.Variant{
			{ID: "v1", Provider: "fake", Model: "a", Params: json.RawMessage(`{"temperature":0.1}`)},
			{ID: "v2", Provider: "fake", Model: "b", Params: json.RawMessage(`{"temperature":0.2}`)},
		},
		Cases: []storage.Case{
			{ID: "c1", Name: "one", Messages: json.RawMessage(`[{"role":"user","content":"hi"}]`)},
			{ID: "c2", Name: "two", Messages: json.RawMessage(`[{"role":"user","content":"hello"}]`)},
		},
	}
	if err := store.CreateExperiment(context.Background(), experiment); err != nil {
		t.Fatal(err)
	}
	return New(store, eng, insp, 2), store, experiment.ID
}

func TestRunnerExecutesMatrix(t *testing.T) {
	runner, store, id := setup(t, 0)
	result, err := runner.Start(id)
	if err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	if result.BatchID == "" || len(result.RunIDs) != 4 {
		t.Errorf("start result = %+v", result)
	}
	runs, err := store.ListRuns(context.Background(), id, result.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("runs = %d", len(runs))
	}
	for _, run := range runs {
		if run.Status != storage.RunSucceeded {
			t.Errorf("run %s status = %s (%s)", run.ID, run.Status, run.Error)
		}
		if run.Output == "" || run.RequestID == "" {
			t.Errorf("run = %+v", run)
		}
	}
	if err := runner.inspector.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests, total, err := store.List(context.Background(), storage.Filter{Source: "experiment"})
	if err != nil || total != 4 || len(requests) != 4 {
		t.Errorf("captured = %d/%d, %v", len(requests), total, err)
	}
}

func TestRunnerCancel(t *testing.T) {
	runner, store, id := setup(t, 200)
	result, err := runner.Start(id)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if !runner.Cancel(id) {
		t.Fatal("Cancel returned false")
	}
	runner.Wait()
	runs, err := store.ListRuns(context.Background(), id, result.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Status == storage.RunPending || run.Status == storage.RunRunning {
			t.Errorf("run %s still %s after cancel", run.ID, run.Status)
		}
	}
}

func TestRunnerRefusesDuplicate(t *testing.T) {
	runner, _, id := setup(t, 100)
	if _, err := runner.Start(id); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(id); err == nil {
		t.Error("duplicate start accepted")
	}
	runner.Cancel(id)
	runner.Wait()
}
