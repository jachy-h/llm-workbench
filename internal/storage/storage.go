// Package storage persists request history in a local SQLite database.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a request ID does not exist.
var ErrNotFound = errors.New("storage: request not found")

// timestampLayout keeps stored timestamps lexicographically sortable even
// when the fractional second is zero.
const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func formatTimestamp(value time.Time) string { return value.UTC().Format(timestampLayout) }

// Record is one captured model call.
type Record struct {
	ID                string
	CreatedAt         time.Time
	Source            string
	Provider          string
	Model             string
	Endpoint          string
	Method            string
	Stream            bool
	StatusCode        int
	Error             string
	DurationMS        int64
	TTFTMS            int64
	PromptTokens      *int
	CompletionTokens  *int
	TotalTokens       *int
	Client            string
	Note              string
	SystemPrompt      string
	RequestOriginal   string
	RequestForwarded  string
	ResponseBody      string
	ResponseTruncated bool
}

// Summary is the list projection: metadata only, no bodies.
type Summary struct {
	ID                string    `json:"id"`
	CreatedAt         time.Time `json:"created_at"`
	Source            string    `json:"source"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Endpoint          string    `json:"endpoint"`
	Method            string    `json:"method"`
	Stream            bool      `json:"stream"`
	StatusCode        int       `json:"status_code,omitempty"`
	Error             string    `json:"error,omitempty"`
	DurationMS        int64     `json:"duration_ms"`
	TTFTMS            int64     `json:"ttft_ms"`
	PromptTokens      *int      `json:"prompt_tokens,omitempty"`
	CompletionTokens  *int      `json:"completion_tokens,omitempty"`
	TotalTokens       *int      `json:"total_tokens,omitempty"`
	Client            string    `json:"client,omitempty"`
	Note              string    `json:"note,omitempty"`
	SystemPrompt      string    `json:"system_prompt,omitempty"`
	ResponseTruncated bool      `json:"response_truncated"`
}

// Filter selects history rows. Zero values mean "no filter".
type Filter struct {
	Limit    int
	Offset   int
	Provider string
	Model    string
	Source   string
	Status   *int
	Since    *time.Time
	Until    *time.Time
	Query    string
}

// Stats is a small overview for the UI.
type Stats struct {
	Count     int64     `json:"count"`
	Oldest    time.Time `json:"oldest,omitempty"`
	Newest    time.Time `json:"newest,omitempty"`
	SizeBytes int64     `json:"size_bytes"`
	Path      string    `json:"path"`
}

// Prompt is a saved system prompt + messages + params bundle.
type Prompt struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	System    string          `json:"system"`
	Messages  json.RawMessage `json:"messages"`
	Params    json.RawMessage `json:"params"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Experiment is a named group of variants and cases.
type Experiment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Variants  []Variant `json:"variants"`
	Cases     []Case    `json:"cases"`
}

// ExperimentSummary is the list projection with counts.
type ExperimentSummary struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Notes        string    `json:"notes"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	VariantCount int       `json:"variant_count"`
	CaseCount    int       `json:"case_count"`
	RunCount     int       `json:"run_count"`
}

// Variant is one model/system/params combination in an experiment.
type Variant struct {
	ID           string          `json:"id"`
	ExperimentID string          `json:"experiment_id"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	System       string          `json:"system"`
	Params       json.RawMessage `json:"params"`
	SortOrder    int             `json:"sort_order"`
}

// Case is one input message set in an experiment.
type Case struct {
	ID           string          `json:"id"`
	ExperimentID string          `json:"experiment_id"`
	Name         string          `json:"name"`
	Messages     json.RawMessage `json:"messages"`
	SortOrder    int             `json:"sort_order"`
}

// Run is one variant × case execution.
type Run struct {
	ID               string     `json:"id"`
	BatchID          string     `json:"batch_id"`
	ExperimentID     string     `json:"experiment_id"`
	VariantID        string     `json:"variant_id"`
	CaseID           string     `json:"case_id"`
	RequestID        string     `json:"request_id"`
	Status           string     `json:"status"`
	Error            string     `json:"error"`
	Output           string     `json:"output"`
	Thinking         string     `json:"thinking"`
	PromptTokens     *int       `json:"prompt_tokens,omitempty"`
	CompletionTokens *int       `json:"completion_tokens,omitempty"`
	DurationMS       int64      `json:"duration_ms"`
	TTFTMS           int64      `json:"ttft_ms"`
	Rating           *int       `json:"rating,omitempty"`
	RatingNote       string     `json:"rating_note"`
	CreatedAt        time.Time  `json:"created_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

// Run statuses.
const (
	RunPending   = "pending"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCanceled  = "canceled"
)

// Store is a SQLite-backed request history.
type Store struct {
	db      *sql.DB
	path    string
	writeMu sync.Mutex
}

// Open creates the directory, opens the database and applies migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("storage: path is empty")
	}
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer. A small pool with WAL is enough for a local
	// workbench and avoids SQLITE_BUSY storms.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if path == ":memory:" {
		// Every new connection to :memory: is a separate database.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("storage: %s: %w", pragma, err)
		}
	}
	store := &Store{db: db, path: path}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the database file.
func (s *Store) Path() string { return s.path }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	for _, migration := range migrations {
		var applied int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, migration.version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, statement := range migration.statements {
			if _, err := tx.Exec(statement); err != nil {
				tx.Rollback()
				return fmt.Errorf("storage: migrate v%d: %w", migration.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			migration.version, formatTimestamp(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type migration struct {
	version    int
	statements []string
}

var migrations = []migration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE IF NOT EXISTS requests (
				id                 TEXT PRIMARY KEY,
				created_at         TEXT NOT NULL,
				source             TEXT NOT NULL,
				provider           TEXT NOT NULL,
				model              TEXT NOT NULL,
				endpoint           TEXT NOT NULL,
				method             TEXT NOT NULL,
				stream             INTEGER NOT NULL DEFAULT 0,
				status_code        INTEGER,
				error              TEXT,
				duration_ms        INTEGER,
				ttft_ms            INTEGER,
				prompt_tokens      INTEGER,
				completion_tokens  INTEGER,
				total_tokens       INTEGER,
				client             TEXT,
				note               TEXT,
				system_prompt      TEXT,
				request_original   TEXT,
				request_forwarded  TEXT,
				response_body      TEXT,
				response_truncated INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status_code, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_requests_source ON requests(source, created_at DESC)`,
		},
	},
	{
		version: 2,
		statements: []string{
			`CREATE TABLE IF NOT EXISTS prompts (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				system     TEXT NOT NULL DEFAULT '',
				messages   TEXT NOT NULL,
				params     TEXT NOT NULL,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_prompts_updated ON prompts(updated_at DESC)`,
		},
	},
	{
		version: 3,
		statements: []string{
			`CREATE TABLE IF NOT EXISTS experiments (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				notes      TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS experiment_variants (
				id            TEXT PRIMARY KEY,
				experiment_id TEXT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
				provider      TEXT NOT NULL,
				model         TEXT NOT NULL,
				system        TEXT NOT NULL DEFAULT '',
				params        TEXT NOT NULL,
				sort_order    INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE TABLE IF NOT EXISTS experiment_cases (
				id            TEXT PRIMARY KEY,
				experiment_id TEXT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
				name          TEXT NOT NULL DEFAULT '',
				messages      TEXT NOT NULL,
				sort_order    INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE TABLE IF NOT EXISTS experiment_runs (
				id                TEXT PRIMARY KEY,
				batch_id          TEXT NOT NULL,
				experiment_id     TEXT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
				variant_id        TEXT NOT NULL REFERENCES experiment_variants(id) ON DELETE CASCADE,
				case_id           TEXT NOT NULL REFERENCES experiment_cases(id) ON DELETE CASCADE,
				request_id        TEXT,
				status            TEXT NOT NULL,
				error             TEXT NOT NULL DEFAULT '',
				output            TEXT NOT NULL DEFAULT '',
				thinking          TEXT NOT NULL DEFAULT '',
				prompt_tokens     INTEGER,
				completion_tokens INTEGER,
				duration_ms       INTEGER,
				ttft_ms           INTEGER,
				rating            INTEGER,
				rating_note       TEXT NOT NULL DEFAULT '',
				created_at        TEXT NOT NULL,
				finished_at       TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS idx_experiments_updated ON experiments(updated_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_variants_experiment ON experiment_variants(experiment_id, sort_order)`,
			`CREATE INDEX IF NOT EXISTS idx_cases_experiment ON experiment_cases(experiment_id, sort_order)`,
			`CREATE INDEX IF NOT EXISTS idx_runs_experiment ON experiment_runs(experiment_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_runs_batch ON experiment_runs(batch_id)`,
		},
	},
}

// Insert stores one record.
func (s *Store) Insert(ctx context.Context, record Record) error {
	if record.ID == "" {
		return errors.New("storage: request id is empty")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO requests (
		id, created_at, source, provider, model, endpoint, method, stream,
		status_code, error, duration_ms, ttft_ms,
		prompt_tokens, completion_tokens, total_tokens,
		client, note, system_prompt,
		request_original, request_forwarded, response_body, response_truncated
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, formatTimestamp(record.CreatedAt),
		record.Source, record.Provider, record.Model, record.Endpoint, record.Method, boolInt(record.Stream),
		record.StatusCode, record.Error, record.DurationMS, record.TTFTMS,
		nullableInt(record.PromptTokens), nullableInt(record.CompletionTokens), nullableInt(record.TotalTokens),
		record.Client, record.Note, record.SystemPrompt,
		record.RequestOriginal, record.RequestForwarded, record.ResponseBody, boolInt(record.ResponseTruncated),
	)
	return err
}

// Get returns the full record or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		id, created_at, source, provider, model, endpoint, method, stream,
		status_code, error, duration_ms, ttft_ms,
		prompt_tokens, completion_tokens, total_tokens,
		client, note, system_prompt,
		request_original, request_forwarded, response_body, response_truncated
		FROM requests WHERE id = ?`, id)
	return scanRecord(row)
}

// List returns metadata rows and the total count matching the filter.
func (s *Store) List(ctx context.Context, filter Filter) ([]Summary, int64, error) {
	where, args := buildWhere(filter)
	countQuery := `SELECT COUNT(*) FROM requests` + where
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query := `SELECT
		id, created_at, source, provider, model, endpoint, method, stream,
		status_code, error, duration_ms, ttft_ms,
		prompt_tokens, completion_tokens, total_tokens,
		client, note, system_prompt, response_truncated
		FROM requests` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Summary, 0, limit)
	for rows.Next() {
		var (
			item          Summary
			created       string
			stream        int
			status        sql.NullInt64
			errText       sql.NullString
			duration      sql.NullInt64
			ttft          sql.NullInt64
			prompt, comp  sql.NullInt64
			totalTokens   sql.NullInt64
			client, note  sql.NullString
			systemPrompt  sql.NullString
			respTruncated int
		)
		if err := rows.Scan(
			&item.ID, &created, &item.Source, &item.Provider, &item.Model, &item.Endpoint, &item.Method, &stream,
			&status, &errText, &duration, &ttft,
			&prompt, &comp, &totalTokens,
			&client, &note, &systemPrompt, &respTruncated,
		); err != nil {
			return nil, 0, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		item.Stream = stream != 0
		item.StatusCode = int(status.Int64)
		item.Error = errText.String
		item.DurationMS = duration.Int64
		item.TTFTMS = ttft.Int64
		item.PromptTokens = intPtr(prompt)
		item.CompletionTokens = intPtr(comp)
		item.TotalTokens = intPtr(totalTokens)
		item.Client = client.String
		item.Note = note.String
		item.SystemPrompt = systemPrompt.String
		item.ResponseTruncated = respTruncated != 0
		out = append(out, item)
	}
	return out, total, rows.Err()
}

// Export returns full records for the filter, bounded by limit.
func (s *Store) Export(ctx context.Context, filter Filter) ([]Record, error) {
	where, args := buildWhere(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	query := `SELECT
		id, created_at, source, provider, model, endpoint, method, stream,
		status_code, error, duration_ms, ttft_ms,
		prompt_tokens, completion_tokens, total_tokens,
		client, note, system_prompt,
		request_original, request_forwarded, response_body, response_truncated
		FROM requests` + where + ` ORDER BY created_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Record, 0, limit)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// Delete removes one record.
func (s *Store) Delete(ctx context.Context, id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// DeleteFiltered removes matching rows. all must be true when the filter is
// empty, forcing the caller to be explicit about deleting everything.
func (s *Store) DeleteFiltered(ctx context.Context, filter Filter, all bool) (int64, error) {
	where, args := buildWhere(filter)
	if where == "" && !all {
		return 0, errors.New("storage: refusing to delete every request without all=true")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM requests`+where, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Prune deletes old rows or keeps only the newest maxRows.
func (s *Store) Prune(ctx context.Context, olderThan time.Time, maxRows int) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var deleted int64
	if !olderThan.IsZero() {
		result, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE created_at < ?`,
			formatTimestamp(olderThan))
		if err != nil {
			return 0, err
		}
		n, _ := result.RowsAffected()
		deleted += n
	}
	if maxRows > 0 {
		result, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE id NOT IN (
			SELECT id FROM requests ORDER BY created_at DESC LIMIT ?
		)`, maxRows)
		if err != nil {
			return deleted, err
		}
		n, _ := result.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

// CreatePrompt stores a new saved prompt.
func (s *Store) CreatePrompt(ctx context.Context, prompt Prompt) error {
	if prompt.ID == "" || prompt.Name == "" {
		return errors.New("storage: prompt id and name are required")
	}
	if !json.Valid(prompt.Messages) || !json.Valid(prompt.Params) {
		return errors.New("storage: prompt messages/params must be valid JSON")
	}
	now := time.Now().UTC()
	if prompt.CreatedAt.IsZero() {
		prompt.CreatedAt = now
	}
	prompt.UpdatedAt = now
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO prompts
		(id, name, system, messages, params, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		prompt.ID, prompt.Name, prompt.System, string(prompt.Messages), string(prompt.Params),
		formatTimestamp(prompt.CreatedAt), formatTimestamp(prompt.UpdatedAt))
	return err
}

// UpdatePrompt replaces an existing saved prompt.
func (s *Store) UpdatePrompt(ctx context.Context, prompt Prompt) error {
	if prompt.ID == "" || prompt.Name == "" {
		return errors.New("storage: prompt id and name are required")
	}
	if !json.Valid(prompt.Messages) || !json.Valid(prompt.Params) {
		return errors.New("storage: prompt messages/params must be valid JSON")
	}
	prompt.UpdatedAt = time.Now().UTC()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE prompts
		SET name = ?, system = ?, messages = ?, params = ?, updated_at = ?
		WHERE id = ?`,
		prompt.Name, prompt.System, string(prompt.Messages), string(prompt.Params),
		formatTimestamp(prompt.UpdatedAt), prompt.ID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPrompt returns one saved prompt.
func (s *Store) GetPrompt(ctx context.Context, id string) (Prompt, error) {
	var (
		prompt   Prompt
		messages string
		params   string
		created  string
		updated  string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, system, messages, params, created_at, updated_at
		FROM prompts WHERE id = ?`, id).
		Scan(&prompt.ID, &prompt.Name, &prompt.System, &messages, &params, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Prompt{}, ErrNotFound
	}
	if err != nil {
		return Prompt{}, err
	}
	prompt.Messages = json.RawMessage(messages)
	prompt.Params = json.RawMessage(params)
	prompt.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	prompt.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return prompt, nil
}

// ListPrompts returns saved prompts, newest update first.
func (s *Store) ListPrompts(ctx context.Context) ([]Prompt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, system, messages, params, created_at, updated_at
		FROM prompts ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Prompt{}
	for rows.Next() {
		var (
			prompt   Prompt
			messages string
			params   string
			created  string
			updated  string
		)
		if err := rows.Scan(&prompt.ID, &prompt.Name, &prompt.System, &messages, &params, &created, &updated); err != nil {
			return nil, err
		}
		prompt.Messages = json.RawMessage(messages)
		prompt.Params = json.RawMessage(params)
		prompt.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		prompt.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, prompt)
	}
	return out, rows.Err()
}

// DeletePrompt removes one saved prompt.
func (s *Store) DeletePrompt(ctx context.Context, id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM prompts WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

// CreateExperiment stores an experiment with its variants and cases atomically.
func (s *Store) CreateExperiment(ctx context.Context, experiment Experiment) error {
	if experiment.ID == "" || experiment.Name == "" {
		return errors.New("storage: experiment id and name are required")
	}
	if len(experiment.Variants) == 0 || len(experiment.Cases) == 0 {
		return errors.New("storage: experiment needs at least one variant and one case")
	}
	now := time.Now().UTC()
	if experiment.CreatedAt.IsZero() {
		experiment.CreatedAt = now
	}
	experiment.UpdatedAt = now
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO experiments(id, name, notes, created_at, updated_at) VALUES (?,?,?,?,?)`,
		experiment.ID, experiment.Name, experiment.Notes,
		formatTimestamp(experiment.CreatedAt), formatTimestamp(experiment.UpdatedAt)); err != nil {
		return err
	}
	for index, variant := range experiment.Variants {
		if variant.ID == "" || variant.Provider == "" || variant.Model == "" || !json.Valid(variant.Params) {
			return errors.New("storage: invalid variant")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_variants
			(id, experiment_id, provider, model, system, params, sort_order) VALUES (?,?,?,?,?,?,?)`,
			variant.ID, experiment.ID, variant.Provider, variant.Model, variant.System,
			string(variant.Params), index); err != nil {
			return err
		}
	}
	for index, item := range experiment.Cases {
		if item.ID == "" || !json.Valid(item.Messages) {
			return errors.New("storage: invalid case")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_cases
			(id, experiment_id, name, messages, sort_order) VALUES (?,?,?,?,?)`,
			item.ID, experiment.ID, item.Name, string(item.Messages), index); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListExperiments returns experiment summaries with counts.
func (s *Store) ListExperiments(ctx context.Context) ([]ExperimentSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.name, e.notes, e.created_at, e.updated_at,
		(SELECT COUNT(*) FROM experiment_variants v WHERE v.experiment_id = e.id),
		(SELECT COUNT(*) FROM experiment_cases c WHERE c.experiment_id = e.id),
		(SELECT COUNT(*) FROM experiment_runs r WHERE r.experiment_id = e.id)
		FROM experiments e ORDER BY e.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExperimentSummary{}
	for rows.Next() {
		var item ExperimentSummary
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &item.Notes, &created, &updated,
			&item.VariantCount, &item.CaseCount, &item.RunCount); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetExperiment returns one experiment with variants and cases.
func (s *Store) GetExperiment(ctx context.Context, id string) (Experiment, error) {
	var (
		experiment Experiment
		created    string
		updated    string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, notes, created_at, updated_at FROM experiments WHERE id = ?`, id).
		Scan(&experiment.ID, &experiment.Name, &experiment.Notes, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Experiment{}, ErrNotFound
	}
	if err != nil {
		return Experiment{}, err
	}
	experiment.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	experiment.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)

	variantRows, err := s.db.QueryContext(ctx, `SELECT id, experiment_id, provider, model, system, params, sort_order
		FROM experiment_variants WHERE experiment_id = ? ORDER BY sort_order`, id)
	if err != nil {
		return Experiment{}, err
	}
	for variantRows.Next() {
		var item Variant
		var params string
		if err := variantRows.Scan(&item.ID, &item.ExperimentID, &item.Provider, &item.Model,
			&item.System, &params, &item.SortOrder); err != nil {
			variantRows.Close()
			return Experiment{}, err
		}
		item.Params = json.RawMessage(params)
		experiment.Variants = append(experiment.Variants, item)
	}
	variantRows.Close()
	if err := variantRows.Err(); err != nil {
		return Experiment{}, err
	}

	caseRows, err := s.db.QueryContext(ctx, `SELECT id, experiment_id, name, messages, sort_order
		FROM experiment_cases WHERE experiment_id = ? ORDER BY sort_order`, id)
	if err != nil {
		return Experiment{}, err
	}
	defer caseRows.Close()
	for caseRows.Next() {
		var item Case
		var messages string
		if err := caseRows.Scan(&item.ID, &item.ExperimentID, &item.Name, &messages, &item.SortOrder); err != nil {
			return Experiment{}, err
		}
		item.Messages = json.RawMessage(messages)
		experiment.Cases = append(experiment.Cases, item)
	}
	return experiment, caseRows.Err()
}

// UpdateExperiment changes the name and notes.
func (s *Store) UpdateExperiment(ctx context.Context, id, name, notes string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("storage: experiment name is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE experiments SET name = ?, notes = ?, updated_at = ? WHERE id = ?`,
		name, notes, formatTimestamp(time.Now()), id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExperiment removes an experiment and its children.
func (s *Store) DeleteExperiment(ctx context.Context, id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM experiments WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

// CreateRuns inserts a batch of pending runs.
func (s *Store) CreateRuns(ctx context.Context, runs []Run) error {
	if len(runs) == 0 {
		return errors.New("storage: no runs")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, run := range runs {
		if run.ID == "" || run.BatchID == "" || run.ExperimentID == "" {
			return errors.New("storage: invalid run")
		}
		if run.CreatedAt.IsZero() {
			run.CreatedAt = time.Now().UTC()
		}
		if run.Status == "" {
			run.Status = RunPending
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_runs
			(id, batch_id, experiment_id, variant_id, case_id, status, created_at) VALUES (?,?,?,?,?,?,?)`,
			run.ID, run.BatchID, run.ExperimentID, run.VariantID, run.CaseID, run.Status,
			formatTimestamp(run.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListRuns returns runs for an experiment, optionally filtered by batch.
func (s *Store) ListRuns(ctx context.Context, experimentID, batchID string) ([]Run, error) {
	query := `SELECT id, batch_id, experiment_id, variant_id, case_id, request_id, status, error,
		output, thinking, prompt_tokens, completion_tokens, duration_ms, ttft_ms,
		rating, rating_note, created_at, finished_at
		FROM experiment_runs WHERE experiment_id = ?`
	args := []any{experimentID}
	if batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	query += ` ORDER BY created_at, variant_id, case_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// UpdateRun stores progress or a final result. Rating fields are untouched.
func (s *Store) UpdateRun(ctx context.Context, run Run) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var finished any
	if run.FinishedAt != nil {
		finished = formatTimestamp(*run.FinishedAt)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE experiment_runs SET
		request_id = ?, status = ?, error = ?, output = ?, thinking = ?,
		prompt_tokens = ?, completion_tokens = ?, duration_ms = ?, ttft_ms = ?, finished_at = ?
		WHERE id = ?`,
		run.RequestID, run.Status, run.Error, run.Output, run.Thinking,
		nullableInt(run.PromptTokens), nullableInt(run.CompletionTokens), run.DurationMS, run.TTFTMS,
		finished, run.ID)
	return err
}

// SetRunRating stores a manual 0–5 rating and note.
func (s *Store) SetRunRating(ctx context.Context, id string, rating *int, note string) error {
	if rating != nil && (*rating < 0 || *rating > 5) {
		return errors.New("storage: rating must be between 0 and 5")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE experiment_runs SET rating = ?, rating_note = ? WHERE id = ?`,
		nullableInt(rating), note, id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelBatch marks pending/running rows as canceled.
func (s *Store) CancelBatch(ctx context.Context, batchID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE experiment_runs
		SET status = ?, error = 'canceled', finished_at = ?
		WHERE batch_id = ? AND status IN (?, ?)`,
		RunCanceled, formatTimestamp(time.Now()), batchID, RunPending, RunRunning)
	return err
}

func scanRun(row interface{ Scan(dest ...any) error }) (Run, error) {
	var (
		run        Run
		requestID  sql.NullString
		errText    sql.NullString
		output     sql.NullString
		thinking   sql.NullString
		prompt     sql.NullInt64
		completion sql.NullInt64
		duration   sql.NullInt64
		ttft       sql.NullInt64
		rating     sql.NullInt64
		ratingNote sql.NullString
		created    string
		finished   sql.NullString
	)
	err := row.Scan(&run.ID, &run.BatchID, &run.ExperimentID, &run.VariantID, &run.CaseID,
		&requestID, &run.Status, &errText, &output, &thinking,
		&prompt, &completion, &duration, &ttft, &rating, &ratingNote, &created, &finished)
	if err != nil {
		return Run{}, err
	}
	run.RequestID = requestID.String
	run.Error = errText.String
	run.Output = output.String
	run.Thinking = thinking.String
	run.PromptTokens = intPtr(prompt)
	run.CompletionTokens = intPtr(completion)
	run.DurationMS = duration.Int64
	run.TTFTMS = ttft.Int64
	run.Rating = intPtr(rating)
	run.RatingNote = ratingNote.String
	run.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if finished.Valid {
		value, _ := time.Parse(time.RFC3339Nano, finished.String)
		run.FinishedAt = &value
	}
	return run, nil
}

// GetRun returns one run.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, batch_id, experiment_id, variant_id, case_id, request_id, status, error,
		output, thinking, prompt_tokens, completion_tokens, duration_ms, ttft_ms,
		rating, rating_note, created_at, finished_at
		FROM experiment_runs WHERE id = ?`, id)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return run, err
}

// Stats returns a small overview.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{Path: s.path}
	var oldest, newest sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM requests`).
		Scan(&stats.Count, &oldest, &newest); err != nil {
		return stats, err
	}
	if oldest.Valid {
		stats.Oldest, _ = time.Parse(time.RFC3339Nano, oldest.String)
	}
	if newest.Valid {
		stats.Newest, _ = time.Parse(time.RFC3339Nano, newest.String)
	}
	if s.path != ":memory:" {
		if info, err := os.Stat(s.path); err == nil {
			stats.SizeBytes = info.Size()
		}
	}
	return stats, nil
}

func buildWhere(filter Filter) (string, []any) {
	var clauses []string
	var args []any
	if filter.Provider != "" {
		clauses = append(clauses, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.Model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, filter.Model)
	}
	if filter.Source != "" {
		clauses = append(clauses, "source = ?")
		args = append(args, filter.Source)
	}
	if filter.Status != nil {
		clauses = append(clauses, "status_code = ?")
		args = append(args, *filter.Status)
	}
	if filter.Since != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, formatTimestamp(*filter.Since))
	}
	if filter.Until != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, formatTimestamp(*filter.Until))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		pattern := "%" + query + "%"
		clauses = append(clauses, "(system_prompt LIKE ? OR request_original LIKE ? OR request_forwarded LIKE ? OR response_body LIKE ? OR model LIKE ?)")
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanRecord(row interface {
	Scan(dest ...any) error
}) (Record, error) {
	var (
		record        Record
		created       string
		stream        int
		status        sql.NullInt64
		errText       sql.NullString
		duration      sql.NullInt64
		ttft          sql.NullInt64
		prompt, comp  sql.NullInt64
		totalTokens   sql.NullInt64
		client, note  sql.NullString
		systemPrompt  sql.NullString
		orig, forward sql.NullString
		respBody      sql.NullString
		respTruncated int
	)
	err := row.Scan(
		&record.ID, &created, &record.Source, &record.Provider, &record.Model, &record.Endpoint, &record.Method, &stream,
		&status, &errText, &duration, &ttft,
		&prompt, &comp, &totalTokens,
		&client, &note, &systemPrompt,
		&orig, &forward, &respBody, &respTruncated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	record.Stream = stream != 0
	record.StatusCode = int(status.Int64)
	record.Error = errText.String
	record.DurationMS = duration.Int64
	record.TTFTMS = ttft.Int64
	record.PromptTokens = intPtr(prompt)
	record.CompletionTokens = intPtr(comp)
	record.TotalTokens = intPtr(totalTokens)
	record.Client = client.String
	record.Note = note.String
	record.SystemPrompt = systemPrompt.String
	record.RequestOriginal = orig.String
	record.RequestForwarded = forward.String
	record.ResponseBody = respBody.String
	record.ResponseTruncated = respTruncated != 0
	return record, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func intPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}
