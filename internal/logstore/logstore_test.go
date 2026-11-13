package logstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLog(t *testing.T, capacity int) *Log {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxy.log")
	log := New(path, capacity)
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestOnlyLatestEntriesAreKept(t *testing.T) {
	log := newLog(t, 5)
	for i := 0; i < 9; i++ {
		log.Write(fmt.Sprintf("line %d", i), "", "")
	}
	result := log.Entries(0)
	if len(result.Entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(result.Entries))
	}
	for i, entry := range result.Entries {
		want := fmt.Sprintf("line %d", i+4)
		if entry.Text != want {
			t.Errorf("entry[%d] = %q, want %q", i, entry.Text, want)
		}
	}
	if got := fileLines(t, log.Path()); len(got) != 5 {
		t.Errorf("file has %d lines, want 5: %v", len(got), got)
	}
}

func TestDefaultCapacity(t *testing.T) {
	log := newLog(t, 0)
	if log.Capacity() != DefaultCapacity {
		t.Fatalf("capacity = %d, want %d", log.Capacity(), DefaultCapacity)
	}
	for i := 0; i < 150; i++ {
		log.Write(fmt.Sprintf("n%d", i), "", "")
	}
	if got := len(log.Entries(0).Entries); got != 100 {
		t.Errorf("entries = %d, want 100", got)
	}
}

func TestIncrementalEntries(t *testing.T) {
	log := newLog(t, 0)
	first := log.Write("a", "", "")
	log.Write("b", "", "")
	third := log.Write("c", "", "")

	all := log.Entries(0)
	if len(all.Entries) != 3 || all.Latest != third.Seq {
		t.Fatalf("all = %+v", all)
	}
	tail := log.Entries(first.Seq)
	if len(tail.Entries) != 2 || tail.Entries[0].Text != "b" || tail.Entries[1].Text != "c" {
		t.Fatalf("tail = %+v", tail.Entries)
	}
	if tail.Latest != third.Seq {
		t.Errorf("latest = %d, want %d", tail.Latest, third.Seq)
	}
	if got := log.Entries(third.Seq).Entries; len(got) != 0 {
		t.Errorf("entries after latest = %v", got)
	}
}

func TestSinceOlderThanWindowReturnsEverything(t *testing.T) {
	log := newLog(t, 3)
	for i := 0; i < 6; i++ {
		log.Write(fmt.Sprintf("l%d", i), "", "")
	}
	// Seq 1..3 rolled out of the window; the portal must resync.
	if got := len(log.Entries(1).Entries); got != 3 {
		t.Errorf("entries = %d, want full window 3", got)
	}
	if got := len(log.Entries(100).Entries); got != 3 {
		t.Errorf("future since should resync, got %d", got)
	}
}

func TestRequestEntriesAreStructured(t *testing.T) {
	log := newLog(t, 0)
	ok := log.WriteRequest("POST", "/v1/chat/completions", 200, "injected:v1", "127.0.0.1", "")
	if ok.Kind != "request" || ok.Level != "ok" || ok.Status != 200 || ok.Method != "POST" {
		t.Errorf("entry = %+v", ok)
	}
	if !strings.Contains(ok.Text, "injected:v1") || !strings.Contains(ok.Text, "127.0.0.1") {
		t.Errorf("text = %q", ok.Text)
	}

	if got := log.WriteRequest("GET", "/api/tags", 404, "", "", "").Level; got != "warn" {
		t.Errorf("404 level = %q, want warn", got)
	}
	if got := log.WriteRequest("GET", "/api/tags", 502, "", "", "").Level; got != "error" {
		t.Errorf("502 level = %q, want error", got)
	}
	if got := log.WriteRequest("GET", "/api/tags", 200, "pass", "", "").Level; got != "info" {
		t.Errorf("pass level = %q, want info", got)
	}
	if got := log.Write("上游超时", "", "").Level; got != "error" {
		t.Errorf("text level = %q, want error", got)
	}
}

func TestRestartReloadsRecentLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	first := New(path, 5)
	for i := 0; i < 7; i++ {
		first.Write(fmt.Sprintf("line %d", i), "", "")
	}
	_ = first.Close()

	reloaded := New(path, 5)
	defer reloaded.Close()
	result := reloaded.Entries(0)
	if len(result.Entries) != 5 {
		t.Fatalf("entries = %d, want 5: %+v", len(result.Entries), result.Entries)
	}
	if result.Entries[0].Text != "line 2" || result.Entries[4].Text != "line 6" {
		t.Errorf("reloaded = %v", result.Entries)
	}
	if result.Entries[0].Time == "" || len(result.Entries[0].Time) != stampLen {
		t.Errorf("time = %q", result.Entries[0].Time)
	}
}

func TestTailCompatibility(t *testing.T) {
	log := newLog(t, 5)
	for i := 0; i < 7; i++ {
		log.Write(fmt.Sprintf("line %d", i), "", "")
	}
	tail := log.Tail(3)
	lines := strings.Split(tail, "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "line 6") {
		t.Errorf("tail = %q", tail)
	}
	if got := log.Tail(0); got == "" {
		t.Error("Tail(0) should default to the window")
	}
}

func TestLevelHints(t *testing.T) {
	cases := map[string]string{
		"connection refused":                "error",
		"Traceback (most recent call last)": "error",
		"警告: 端口被占用":                         "warn",
		"全部正常":                              "info",
	}
	for text, want := range cases {
		if got := LevelFor(text, "info"); got != want {
			t.Errorf("LevelFor(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestStartupTrimsPreexistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "2024-01-01 00:00:%02d line %d\n", i, i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	log := New(path, 3)
	defer log.Close()

	entries := log.Entries(0).Entries
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[2].Text != "line 9" || entries[0].Text != "line 7" {
		t.Errorf("entries = %v", entries)
	}
	if got := fileLines(t, path); len(got) != 3 {
		t.Errorf("file has %d lines, want 3: %v", len(got), got)
	}
}

func TestTailReadsOnlyTheEndOfALargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	var b strings.Builder
	for i := 0; i < 20_000; i++ {
		fmt.Fprintf(&b, "2024-01-01 00:00:00 filler line %05d padding padding\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	log := New(path, 5)
	defer log.Close()

	lines := strings.Split(log.Tail(2), "\n")
	if len(lines) != 2 {
		t.Fatalf("tail lines = %d, want 2", len(lines))
	}
	if !strings.HasSuffix(lines[1], "padding") || !strings.Contains(lines[1], "19999") {
		t.Errorf("tail = %q", lines)
	}
}
