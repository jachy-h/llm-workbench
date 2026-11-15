// Package experiments runs variant × case batches through the engine and
// persists each result.
package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jachy-h/llm-workbench/internal/engine"
	"github.com/jachy-h/llm-workbench/internal/inspect"
	"github.com/jachy-h/llm-workbench/internal/inspector"
	"github.com/jachy-h/llm-workbench/internal/policy"
	"github.com/jachy-h/llm-workbench/internal/provider"
	"github.com/jachy-h/llm-workbench/internal/requestid"
	"github.com/jachy-h/llm-workbench/internal/storage"
)

// Runner owns active experiment batches.
type Runner struct {
	store     *storage.Store
	engine    *engine.Engine
	inspector *inspector.Inspector
	workers   int

	mu     sync.Mutex
	active map[string]*activeBatch
	wg     sync.WaitGroup
}

type activeBatch struct {
	batchID string
	cancel  context.CancelFunc
}

// StartResult is returned when a batch is accepted.
type StartResult struct {
	BatchID string   `json:"batch_id"`
	RunIDs  []string `json:"run_ids"`
}

// New builds a runner. workers defaults to 2.
func New(store *storage.Store, eng *engine.Engine, insp *inspector.Inspector, workers int) *Runner {
	if workers < 1 {
		workers = 2
	}
	return &Runner{store: store, engine: eng, inspector: insp, workers: workers, active: map[string]*activeBatch{}}
}

// Start creates pending runs and executes them in the background.
func (r *Runner) Start(experimentID string) (StartResult, error) {
	if r.engine == nil || r.store == nil {
		return StartResult{}, errors.New("experiments: engine and store are required")
	}
	r.mu.Lock()
	if _, running := r.active[experimentID]; running {
		r.mu.Unlock()
		return StartResult{}, errors.New("experiments: this experiment is already running")
	}
	r.mu.Unlock()

	experiment, err := r.store.GetExperiment(context.Background(), experimentID)
	if err != nil {
		return StartResult{}, err
	}
	batchID := requestid.NewWithPrefix("bat_")
	runs := make([]storage.Run, 0, len(experiment.Variants)*len(experiment.Cases))
	for _, variant := range experiment.Variants {
		for _, item := range experiment.Cases {
			runs = append(runs, storage.Run{
				ID:           requestid.NewWithPrefix("run_"),
				BatchID:      batchID,
				ExperimentID: experiment.ID,
				VariantID:    variant.ID,
				CaseID:       item.ID,
				Status:       storage.RunPending,
				CreatedAt:    time.Now().UTC(),
			})
		}
	}
	if err := r.store.CreateRuns(context.Background(), runs); err != nil {
		return StartResult{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.active[experimentID] = &activeBatch{batchID: batchID, cancel: cancel}
	r.mu.Unlock()

	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
	}
	r.wg.Add(1)
	go r.execute(ctx, experiment, runs, batchID)
	return StartResult{BatchID: batchID, RunIDs: runIDs}, nil
}

// Cancel stops the active batch for an experiment.
func (r *Runner) Cancel(experimentID string) bool {
	r.mu.Lock()
	batch, ok := r.active[experimentID]
	if ok {
		delete(r.active, experimentID)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	batch.cancel()
	_ = r.store.CancelBatch(context.Background(), batch.batchID)
	return true
}

// Wait blocks until all batches finish. Intended for tests and shutdown.
func (r *Runner) Wait() { r.wg.Wait() }

// Shutdown cancels every active batch and waits for workers.
func (r *Runner) Shutdown() {
	r.mu.Lock()
	batches := make([]*activeBatch, 0, len(r.active))
	for id, batch := range r.active {
		batches = append(batches, batch)
		delete(r.active, id)
	}
	r.mu.Unlock()
	for _, batch := range batches {
		batch.cancel()
		_ = r.store.CancelBatch(context.Background(), batch.batchID)
	}
	r.wg.Wait()
}

// Active reports whether an experiment has a running batch.
func (r *Runner) Active(experimentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[experimentID]
	return ok
}

func (r *Runner) execute(ctx context.Context, experiment storage.Experiment, runs []storage.Run, batchID string) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		if current, ok := r.active[experiment.ID]; ok && current.batchID == batchID {
			delete(r.active, experiment.ID)
		}
		r.mu.Unlock()
	}()

	variants := make(map[string]storage.Variant, len(experiment.Variants))
	for _, variant := range experiment.Variants {
		variants[variant.ID] = variant
	}
	cases := make(map[string]storage.Case, len(experiment.Cases))
	for _, item := range experiment.Cases {
		cases[item.ID] = item
	}

	sem := make(chan struct{}, r.workers)
	var wg sync.WaitGroup
	for _, run := range runs {
		if ctx.Err() != nil {
			break
		}
		variant, variantOK := variants[run.VariantID]
		item, caseOK := cases[run.CaseID]
		if !variantOK || !caseOK {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(run storage.Run, variant storage.Variant, item storage.Case) {
			defer wg.Done()
			defer func() { <-sem }()
			r.executeRun(ctx, run, variant, item)
		}(run, variant, item)
	}
	wg.Wait()
}

func (r *Runner) executeRun(ctx context.Context, run storage.Run, variant storage.Variant, item storage.Case) {
	run.Status = storage.RunRunning
	_ = r.store.UpdateRun(context.Background(), run)

	var params map[string]json.RawMessage
	if err := json.Unmarshal(variant.Params, &params); err != nil {
		r.finish(run, storage.RunFailed, nil, err)
		return
	}
	var messages []provider.Message
	if err := json.Unmarshal(item.Messages, &messages); err != nil {
		r.finish(run, storage.RunFailed, nil, err)
		return
	}
	request := engine.Request{
		Provider:     variant.Provider,
		Model:        variant.Model,
		Messages:     messages,
		Params:       params,
		Thinking:     policy.ThinkingOff,
		RequestID:    requestid.New(),
		SystemPrompt: policy.SystemPrompt{Mode: policy.SystemPromptOff},
	}
	if variant.System != "" {
		request.SystemPrompt = policy.SystemPrompt{Mode: policy.SystemPromptOverride, Text: variant.System}
	}
	original, _ := json.Marshal(map[string]any{
		"provider": variant.Provider,
		"model":    variant.Model,
		"system":   variant.System,
		"messages": messages,
		"params":   params,
	})
	started := time.Now()
	result, err := r.engine.Chat(ctx, request)
	if ctx.Err() != nil {
		run.Status = storage.RunCanceled
		run.Error = "canceled"
		finished := time.Now()
		run.FinishedAt = &finished
		_ = r.store.UpdateRun(context.Background(), run)
		return
	}
	r.capture(started, original, request, result, err)
	if err != nil {
		r.finish(run, storage.RunFailed, &result, err)
		return
	}
	r.finish(run, storage.RunSucceeded, &result, nil)
}

func (r *Runner) finish(run storage.Run, status string, result *engine.Result, runErr error) {
	run.Status = status
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if runErr != nil {
		run.Error = runErr.Error()
	}
	if result != nil {
		run.RequestID = result.RequestID
		run.Output = result.Response.Text
		run.Thinking = result.Response.Thinking
		run.DurationMS = result.Duration.Milliseconds()
		run.TTFTMS = result.TTFT.Milliseconds()
		if result.Response.Usage != nil {
			prompt := result.Response.Usage.PromptTokens
			completion := result.Response.Usage.CompletionTokens
			run.PromptTokens = &prompt
			run.CompletionTokens = &completion
		}
	}
	_ = r.store.UpdateRun(context.Background(), run)
}

func (r *Runner) capture(started time.Time, original []byte, request engine.Request, result engine.Result, runErr error) {
	if r.inspector == nil || !r.inspector.Enabled() {
		return
	}
	forwarded, _ := json.Marshal(result.Prepared)
	providerID := result.Provider
	if providerID == "" {
		providerID = request.Provider
	}
	status := 200
	if runErr != nil {
		status = 502
	}
	r.inspector.Record(inspect.Capture{
		ID:               result.RequestID,
		Started:          started,
		Source:           "experiment",
		Provider:         providerID,
		Method:           "POST",
		Path:             "/api/chat",
		Note:             "experiment",
		Stream:           false,
		Status:           status,
		Duration:         result.Duration,
		TTFT:             result.TTFT,
		RequestOriginal:  original,
		RequestForwarded: forwarded,
		ResponseBody:     result.Response.Raw,
		CaptureBodies:    true,
		Error:            errorString(runErr),
	})
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Summary is a compact result for API responses.
type Summary struct {
	Experiment storage.ExperimentSummary `json:"experiment"`
	Runs       []storage.Run             `json:"runs"`
}

// Describe loads an experiment and its runs.
func (r *Runner) Describe(experimentID, batchID string) (Summary, error) {
	experiment, err := r.store.GetExperiment(context.Background(), experimentID)
	if err != nil {
		return Summary{}, err
	}
	runs, err := r.store.ListRuns(context.Background(), experimentID, batchID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{
		Experiment: storage.ExperimentSummary{
			ID: experiment.ID, Name: experiment.Name, Notes: experiment.Notes,
			CreatedAt: experiment.CreatedAt, UpdatedAt: experiment.UpdatedAt,
			VariantCount: len(experiment.Variants), CaseCount: len(experiment.Cases),
			RunCount: len(runs),
		},
		Runs: runs,
	}, nil
}
