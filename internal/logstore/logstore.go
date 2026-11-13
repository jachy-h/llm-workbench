// Package logstore is the tiny structured request log behind the portal.
//
// It keeps only the most recent 100 entries in memory and trims the log file
// to the same window, so a long-running proxy cannot grow its log forever.
package logstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultCapacity is the number of entries kept in memory and on disk.
	DefaultCapacity = 100
	// tailBytes caps how much of an existing file is read on startup.
	tailBytes = 128 * 1024
	// stampLen is the length of "2006-01-02 15:04:05".
	stampLen = 19
	// stampLayout is the timestamp prefix of every log line.
	stampLayout = "2006-01-02 15:04:05"
)

var errorHints = []string{
	"error", "Error", "ERROR", "failed", "Failed", "失败", "错误", "异常", "超时",
	"refused", "timeout", "Timeout", "Traceback", " 502", " 503", " 504",
}

var warnHints = []string{"warn", "Warn", "WARNING", "warning", "警告", "未加载", "未安装", "中断"}

// Entry is one log line. Request entries also carry structured fields so the
// portal can render method / path / status badges.
type Entry struct {
	Seq    int64  `json:"seq"`
	Time   string `json:"time"`
	Kind   string `json:"kind"`
	Level  string `json:"level"`
	Text   string `json:"text"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`
	Note   string `json:"note,omitempty"`
	Client string `json:"client,omitempty"`
}

// Result is the incremental view the portal polls every second.
type Result struct {
	Entries  []Entry `json:"entries"`
	Oldest   int64   `json:"oldest"`
	Latest   int64   `json:"latest"`
	Capacity int     `json:"capacity"`
}

// Log is a concurrency-safe ring buffer plus its backing file. Write errors
// (disk full, permissions) are deliberately swallowed: logging must never
// break the proxy.
type Log struct {
	mu       sync.Mutex
	path     string
	capacity int
	entries  []Entry
	seq      int64
	file     *os.File
}

// New opens path (creating parents), loads recent lines and trims the file.
func New(path string, capacity int) *Log {
	if capacity < 1 {
		capacity = DefaultCapacity
	}
	abs := path
	if expanded, err := filepath.Abs(expandHome(path)); err == nil {
		abs = expanded
	}
	l := &Log{path: abs, capacity: capacity}
	if f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		l.file = f
	}
	l.loadRecent()
	l.trimFileLocked() // collapse a previously long file to the window
	return l
}

// Path is the absolute log file location.
func (l *Log) Path() string { return l.path }

// Capacity is the maximum number of entries kept.
func (l *Log) Capacity() int { return l.capacity }

// Close releases the file handle.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Write appends a plain line. klass is "text" or "life"; level "" guesses from
// the message.
func (l *Log) Write(message, level, klass string) Entry {
	if klass == "" {
		klass = "text"
	}
	l.mu.Lock()
	entry := l.make(message, level, klass)
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.capacity {
		l.entries = l.entries[len(l.entries)-l.capacity:]
	}
	l.trimFileLocked()
	l.mu.Unlock()
	return entry
}

// WriteRequest records one proxied request. The portal renders these as
// method / path / status badges.
func (l *Log) WriteRequest(method, path string, status int, note, client, detail string) Entry {
	parts := []string{client, method, path, "->", fmt.Sprint(status), "[" + note + "]"}
	text := strings.Join(nonEmpty(parts), " ")
	if detail != "" && detail != "pass(non-json)" {
		text += " " + detail
	}
	l.mu.Lock()
	entry := l.make(text, LevelForStatus(status, note), "request")
	entry.Method = method
	entry.Path = path
	entry.Status = status
	entry.Note = note
	entry.Client = client
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.capacity {
		l.entries = l.entries[len(l.entries)-l.capacity:]
	}
	l.trimFileLocked()
	l.mu.Unlock()
	return entry
}

// Entries returns the latest window, or only entries newer than since.
func (l *Log) Entries(since int64) Result {
	l.mu.Lock()
	items := make([]Entry, len(l.entries))
	copy(items, l.entries)
	l.mu.Unlock()

	var oldest, latest int64
	if len(items) > 0 {
		oldest, latest = items[0].Seq, items[len(items)-1].Seq
	}
	selected := items
	if !(since <= 0 || len(items) == 0 || since < oldest-1 || since > latest) {
		selected = make([]Entry, 0, len(items))
		for _, entry := range items {
			if entry.Seq > since {
				selected = append(selected, entry)
			}
		}
	}
	return Result{Entries: selected, Oldest: oldest, Latest: latest, Capacity: l.capacity}
}

// Tail returns the last lines of the file, or "" when it cannot be read.
func (l *Log) Tail(lines int) string {
	if lines < 1 {
		lines = DefaultCapacity
	}
	if lines > 5000 {
		lines = 5000
	}
	f, err := os.Open(l.path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	truncated := false
	if info.Size() > tailBytes {
		if _, err := f.Seek(info.Size()-tailBytes, io.SeekStart); err == nil {
			truncated = true
		}
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if truncated {
		// Drop the partial first line produced by the seek.
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[index+1:]
		} else {
			text = ""
		}
	}
	all := strings.Split(text, "\n")
	// Drop the trailing empty element from a final newline.
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// LevelFor guesses a level from free text.
func LevelFor(text, fallback string) string {
	for _, hint := range errorHints {
		if strings.Contains(text, hint) {
			return "error"
		}
	}
	for _, hint := range warnHints {
		if strings.Contains(text, hint) {
			return "warn"
		}
	}
	return fallback
}

// LevelForStatus maps an HTTP status (and injection note) to a level.
func LevelForStatus(status int, note string) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "warn"
	case strings.HasPrefix(note, "injected"):
		return "ok"
	default:
		return "info"
	}
}

func (l *Log) make(text, level, klass string) Entry {
	l.seq++
	if level == "" {
		level = LevelFor(text, "info")
	}
	return Entry{
		Seq:   l.seq,
		Time:  time.Now().Format(stampLayout),
		Kind:  klass,
		Level: level,
		Text:  text,
	}
}

// trimFileLocked rewrites the file with the in-memory window. Truncating in
// place keeps the open append handle valid (a rename would not).
func (l *Log) trimFileLocked() {
	if l.file == nil {
		return
	}
	if err := l.file.Truncate(0); err != nil {
		return
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return
	}
	var b strings.Builder
	for _, entry := range l.entries {
		b.WriteString(entry.Time)
		b.WriteByte(' ')
		b.WriteString(entry.Text)
		b.WriteByte('\n')
	}
	_, _ = l.file.WriteString(b.String())
}

func (l *Log) loadRecent() {
	for _, line := range strings.Split(l.Tail(l.capacity), "\n") {
		if line == "" {
			continue
		}
		stamp := time.Now().Format(stampLayout)
		text := line
		if len(line) > stampLen && line[stampLen] == ' ' {
			stamp, text = line[:stampLen], line[stampLen+1:]
		}
		l.seq++
		l.entries = append(l.entries, Entry{
			Seq:   l.seq,
			Time:  stamp,
			Kind:  "text",
			Level: LevelFor(text, "info"),
			Text:  text,
		})
	}
	if len(l.entries) > l.capacity {
		l.entries = l.entries[len(l.entries)-l.capacity:]
	}
}

func nonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func expandHome(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
