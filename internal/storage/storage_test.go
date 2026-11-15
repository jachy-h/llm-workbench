package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func intp(value int) *int { return &value }

func sample(id string, created time.Time) Record {
	return Record{
		ID:                id,
		CreatedAt:         created,
		Source:            "proxy",
		Provider:          "ollama",
		Model:             "qwen3:8b",
		Endpoint:          "/v1/chat/completions",
		Method:            "POST",
		Stream:            true,
		StatusCode:        200,
		DurationMS:        1234,
		TTFTMS:            210,
		PromptTokens:      intp(10),
		CompletionTokens:  intp(20),
		TotalTokens:       intp(30),
		Client:            "127.0.0.1",
		Note:              "injected:v1",
		SystemPrompt:      "be terse",
		RequestOriginal:   `{"model":"qwen3:8b"}`,
		RequestForwarded:  `{"model":"qwen3:8b","reasoning_effort":"none"}`,
		ResponseBody:      `data: {"token":"hi"}`,
		ResponseTruncated: false,
	}
}

func TestInsertGetList(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	records := []Record{
		sample("req_a", now.Add(-2*time.Minute)),
		sample("req_b", now.Add(-time.Minute)),
		sample("req_c", now),
	}
	records[1].Provider = "other"
	records[1].Model = "m2"
	records[1].StatusCode = 500
	records[1].Stream = false
	records[1].Error = "boom"
	records[1].PromptTokens = nil
	for _, record := range records {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatalf("Insert(%s): %v", record.ID, err)
		}
	}

	got, err := store.Get(ctx, "req_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "qwen3:8b" || got.PromptTokens == nil || *got.PromptTokens != 10 {
		t.Errorf("record = %+v", got)
	}
	if got.RequestOriginal != records[0].RequestOriginal || got.ResponseBody != records[0].ResponseBody {
		t.Errorf("bodies = %q / %q", got.RequestOriginal, got.ResponseBody)
	}
	if _, err := store.Get(ctx, "missing"); err != ErrNotFound {
		t.Errorf("missing err = %v", err)
	}

	list, total, err := store.List(ctx, Filter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(list) != 2 {
		t.Fatalf("total=%d len=%d", total, len(list))
	}
	if list[0].ID != "req_c" || list[1].ID != "req_b" {
		t.Errorf("order = %v, %v", list[0].ID, list[1].ID)
	}
	encoded, _ := json.Marshal(list[0])
	if strings.Contains(string(encoded), "reasoning_effort") {
		t.Error("list projection must not include bodies")
	}

	list, total, err = store.List(ctx, Filter{Provider: "other"})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != "req_b" {
		t.Errorf("provider filter: total=%d list=%v err=%v", total, list, err)
	}
	list, _, err = store.List(ctx, Filter{Status: intp(500)})
	if err != nil || len(list) != 1 || list[0].Error != "boom" {
		t.Errorf("status filter: %v err=%v", list, err)
	}
	list, _, err = store.List(ctx, Filter{Query: "be terse"})
	if err != nil || len(list) != 3 {
		t.Errorf("query filter: len=%d err=%v", len(list), err)
	}
	list, _, err = store.List(ctx, Filter{Query: "reasoning_effort"})
	if err != nil || len(list) != 3 {
		t.Errorf("body query filter: len=%d err=%v", len(list), err)
	}
	list, _, err = store.List(ctx, Filter{Since: &now})
	if err != nil || len(list) != 1 || list[0].ID != "req_c" {
		t.Errorf("since filter: %v err=%v", list, err)
	}
}

func TestExportDeleteAndPrune(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index, id := range []string{"old", "middle", "new"} {
		if err := store.Insert(ctx, sample(id, now.Add(time.Duration(index-2)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	exported, err := store.Export(ctx, Filter{Limit: 10})
	if err != nil || len(exported) != 3 {
		t.Fatalf("export len=%d err=%v", len(exported), err)
	}
	if exported[0].ID != "new" || exported[0].RequestForwarded == "" {
		t.Errorf("export = %+v", exported[0])
	}

	ok, err := store.Delete(ctx, "middle")
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	ok, err = store.Delete(ctx, "middle")
	if err != nil || ok {
		t.Errorf("second Delete: ok=%v err=%v", ok, err)
	}

	// Refuse a blanket delete unless explicitly requested.
	if _, err := store.DeleteFiltered(ctx, Filter{}, false); err == nil {
		t.Error("empty DeleteFiltered succeeded without all=true")
	}
	deleted, err := store.DeleteFiltered(ctx, Filter{Provider: "ollama"}, true)
	if err != nil || deleted != 2 {
		t.Errorf("DeleteFiltered = %d, %v", deleted, err)
	}
}

func TestPrune(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index := 0; index < 5; index++ {
		if err := store.Insert(ctx, sample("req_"+string(rune('a'+index)), now.Add(time.Duration(index-5)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.Prune(ctx, now.Add(-90*time.Minute), 0)
	if err != nil || deleted != 4 {
		t.Fatalf("age prune = %d, %v", deleted, err)
	}
	for index := 0; index < 3; index++ {
		if err := store.Insert(ctx, sample("extra_"+string(rune('a'+index)), now.Add(time.Duration(index)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err = store.Prune(ctx, time.Time{}, 2)
	if err != nil || deleted != 2 {
		t.Fatalf("count prune = %d, %v", deleted, err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Count != 2 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	if stats.Newest.IsZero() || stats.Path == "" {
		t.Errorf("stats = %+v", stats)
	}
}

func TestExperimentCRUD(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	experiment := Experiment{
		ID:    "exp_a",
		Name:  "terse vs verbose",
		Notes: "compare",
		Variants: []Variant{
			{ID: "var_a", Provider: "ollama", Model: "m1", System: "short", Params: json.RawMessage(`{"temperature":0.1}`)},
			{ID: "var_b", Provider: "ollama", Model: "m2", System: "long", Params: json.RawMessage(`{"temperature":0.7}`)},
		},
		Cases: []Case{
			{ID: "case_a", Name: "greeting", Messages: json.RawMessage(`[{"role":"user","content":"hi"}]`)},
			{ID: "case_b", Name: "code", Messages: json.RawMessage(`[{"role":"user","content":"code"}]`)},
		},
	}
	if err := store.CreateExperiment(ctx, experiment); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetExperiment(ctx, "exp_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "terse vs verbose" || len(got.Variants) != 2 || len(got.Cases) != 2 {
		t.Errorf("experiment = %+v", got)
	}
	if got.Variants[0].ID != "var_a" || got.Cases[1].ID != "case_b" {
		t.Errorf("ordering = %+v", got)
	}
	list, err := store.ListExperiments(ctx)
	if err != nil || len(list) != 1 || list[0].VariantCount != 2 || list[0].CaseCount != 2 {
		t.Errorf("list = %+v, %v", list, err)
	}
	if err := store.UpdateExperiment(ctx, "exp_a", "renamed", "notes"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetExperiment(ctx, "exp_a")
	if got.Name != "renamed" || got.Notes != "notes" {
		t.Errorf("updated = %+v", got)
	}

	runs := []Run{
		{ID: "run_a", BatchID: "bat_a", ExperimentID: "exp_a", VariantID: "var_a", CaseID: "case_a", Status: RunPending},
		{ID: "run_b", BatchID: "bat_a", ExperimentID: "exp_a", VariantID: "var_b", CaseID: "case_b", Status: RunPending},
	}
	if err := store.CreateRuns(ctx, runs); err != nil {
		t.Fatal(err)
	}
	listRuns, err := store.ListRuns(ctx, "exp_a", "bat_a")
	if err != nil || len(listRuns) != 2 {
		t.Fatalf("runs = %+v, %v", listRuns, err)
	}
	run := listRuns[0]
	finished := time.Now()
	run.Status = RunSucceeded
	run.Output = "hello"
	run.DurationMS = 100
	run.TTFTMS = 20
	run.FinishedAt = &finished
	if err := store.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	gotRun, err := store.GetRun(ctx, run.ID)
	if err != nil || gotRun.Status != RunSucceeded || gotRun.Output != "hello" || gotRun.FinishedAt == nil {
		t.Errorf("run = %+v, %v", gotRun, err)
	}
	if err := store.SetRunRating(ctx, run.ID, intp(4), "good"); err != nil {
		t.Fatal(err)
	}
	gotRun, _ = store.GetRun(ctx, run.ID)
	if gotRun.Rating == nil || *gotRun.Rating != 4 || gotRun.RatingNote != "good" {
		t.Errorf("rating = %+v", gotRun)
	}
	if err := store.SetRunRating(ctx, run.ID, intp(9), ""); err == nil {
		t.Error("out-of-range rating accepted")
	}

	if err := store.CancelBatch(ctx, "bat_a"); err != nil {
		t.Fatal(err)
	}
	gotRun, _ = store.GetRun(ctx, "run_b")
	if gotRun.Status != RunCanceled {
		t.Errorf("pending run status = %q", gotRun.Status)
	}

	if deleted, err := store.DeleteExperiment(ctx, "exp_a"); err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if _, err := store.GetExperiment(ctx, "exp_a"); err != ErrNotFound {
		t.Errorf("get after delete = %v", err)
	}
	if _, err := store.GetRun(ctx, "run_a"); err != ErrNotFound {
		t.Errorf("run not cascaded: %v", err)
	}
}

func TestExperimentValidation(t *testing.T) {
	store := openTest(t)
	if err := store.CreateExperiment(context.Background(), Experiment{Name: "x"}); err == nil {
		t.Error("experiment without id/variants/cases accepted")
	}
}

func TestPromptCRUD(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	prompt := Prompt{
		ID:       "prm_a",
		Name:     "terse",
		System:   "Be terse.",
		Messages: json.RawMessage(`[{"role":"user","content":"hi"}]`),
		Params:   json.RawMessage(`{"temperature":0.1}`),
	}
	if err := store.CreatePrompt(ctx, prompt); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPrompt(ctx, "prm_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "terse" || got.System != "Be terse." || string(got.Params) != `{"temperature":0.1}` {
		t.Errorf("prompt = %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps = %v / %v", got.CreatedAt, got.UpdatedAt)
	}

	list, err := store.ListPrompts(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	got.Name = "updated"
	got.Params = json.RawMessage(`{"temperature":0.7}`)
	if err := store.UpdatePrompt(ctx, got); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetPrompt(ctx, "prm_a")
	if updated.Name != "updated" || string(updated.Params) != `{"temperature":0.7}` {
		t.Errorf("updated = %+v", updated)
	}

	deleted, err := store.DeletePrompt(ctx, "prm_a")
	if err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if _, err := store.GetPrompt(ctx, "prm_a"); err != ErrNotFound {
		t.Errorf("get after delete = %v", err)
	}
	if err := store.UpdatePrompt(ctx, prompt); err != ErrNotFound {
		t.Errorf("update missing = %v", err)
	}
}

func TestPromptValidation(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	for _, prompt := range []Prompt{
		{Name: "x", Messages: json.RawMessage(`[]`), Params: json.RawMessage(`{}`)},
		{ID: "p", Messages: json.RawMessage(`[]`), Params: json.RawMessage(`{}`)},
		{ID: "p", Name: "x", Messages: json.RawMessage(`not`), Params: json.RawMessage(`{}`)},
		{ID: "p", Name: "x", Messages: json.RawMessage(`[]`), Params: json.RawMessage(`not`)},
	} {
		if err := store.CreatePrompt(ctx, prompt); err == nil {
			t.Errorf("CreatePrompt(%+v) succeeded", prompt)
		}
	}
}

func TestMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbench.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	if err := store.Insert(context.Background(), sample("req_a", time.Now())); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentInsert(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for index := 0; index < 40; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			record := sample(fmt.Sprintf("req_%02d", index), time.Now())
			if err := store.Insert(ctx, record); err != nil {
				errs <- err
			}
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	list, total, err := store.List(ctx, Filter{Limit: 100})
	if err != nil || total != 40 || len(list) != 40 {
		t.Errorf("total=%d len=%d err=%v", total, len(list), err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Error("Open(\"\") succeeded")
	}
}
