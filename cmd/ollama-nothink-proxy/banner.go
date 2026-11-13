package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jachy-h/ollama-nothink-proxy/internal/config"
	"github.com/jachy-h/ollama-nothink-proxy/internal/i18n"
)

// bannerIndent keeps the startup block off the left edge so it reads as one
// card in the terminal.
const bannerIndent = "  "

// labelWidth is the column the values start at (widest label is "upstream").
const (
	labelWidth = 10
	// hintWidth aligns the two closing hint rows.
	hintWidth = 15
)

// paint renders ANSI colour, but only when the writer really is a terminal.
// The detached child writes the same banner into a log file, where escapes
// would just be noise.
type paint struct{ color bool }

func newPaint(w io.Writer) paint {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return paint{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return paint{}
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return paint{}
	}
	return paint{color: true}
}

func (p paint) wrap(code, text string) string {
	if !p.color || text == "" {
		return text
	}
	return code + text + "\x1b[0m"
}

func (p paint) bold(text string) string  { return p.wrap("\x1b[1m", text) }
func (p paint) dim(text string) string   { return p.wrap("\x1b[2m", text) }
func (p paint) green(text string) string { return p.wrap("\x1b[32m", text) }
func (p paint) cyan(text string) string  { return p.wrap("\x1b[36m", text) }
func (p paint) accent(text string) string {
	return p.wrap("\x1b[1;36m", text)
}

// link makes the URL clickable in terminals that understand OSC 8; the text
// itself is what gets printed, so plain terminals lose nothing.
func (p paint) link(url string) string {
	text := p.cyan(url)
	if !p.color {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// bannerOptions carries what differs per launch mode.
type bannerOptions struct {
	// Status is the line under the title, e.g. "后台运行 (pid 92012)".
	Status string
	// Reused marks a launch that found an instance already listening.
	Reused bool
	// Note is an optional dimmed line under the status (e.g. "已停掉旧实例").
	Note string
	// Stop is the hint telling the user how to shut this instance down.
	Stop string
}

// printBanner writes the startup block: what is running, where, how it was
// configured, and how to stop it.
func printBanner(w io.Writer, cfg *config.Config, portalURL string, opts bannerOptions) {
	p := newPaint(w)
	line := func(format string, args ...any) {
		fmt.Fprint(w, bannerIndent)
		fmt.Fprintf(w, format, args...)
		fmt.Fprint(w, "\n")
	}
	kv := func(label, value string) {
		line("%s%s", p.dim(pad(label, labelWidth)), value)
	}

	dot := p.green("●")
	if opts.Reused {
		dot = p.cyan("●")
	}
	fmt.Fprintln(w)
	line("%s %s", p.bold("ollama-nothink-proxy"), p.dim("v"+strings.TrimPrefix(config.Version, "v")))
	line("%s %s", dot, p.bold(opts.Status))
	if opts.Note != "" {
		line("  %s", p.dim(opts.Note))
	}
	fmt.Fprintln(w)

	kv("listen", p.link(cfg.ListenURL()))
	kv("portal", p.link(portalURL))
	kv("upstream", cfg.Upstream())
	kv("inject", injectSummary(p, cfg))
	kv("limits", i18n.T("banner.limits",
		cfg.MaxConcurrent, humanBytes(cfg.MaxBodySize), shortDuration(cfg.Timeout())))
	kv("log", prettyPath(cfg.LogPath))
	kv("config", prettyPath(cfg.ConfigPath))
	kv("verbose", verboseLabel(p, cfg.Verbose))

	fmt.Fprintln(w)
	line("%s %s", p.dim(pad(i18n.T("banner.hintWhere"), hintWidth)), p.dim("→ ")+p.link(portalURL))
	if opts.Stop != "" {
		line("%s %s", p.dim(pad(i18n.T("banner.hintStop"), hintWidth)), opts.Stop)
	}
	fmt.Fprintln(w)
}

// pad left-aligns a label and fills with spaces so all values share a column.
// Width is measured in terminal cells, because CJK labels are two cells each.
func pad(label string, width int) string {
	gap := width - displayWidth(label)
	if gap < 1 {
		gap = 1
	}
	return label + strings.Repeat(" ", gap)
}

// displayWidth counts the terminal cells a string occupies.
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		if isWide(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals … Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F9FF: // emoji
		return true
	}
	return false
}

// injectSummary renders the injected generation params plus the always-on
// thinking switch.
func injectSummary(p paint, cfg *config.Config) string {
	body := cfg.ParamSummary()
	if body == "" {
		body = i18n.T("common.none")
	}
	return body + " " + p.accent(i18n.T("banner.forceOff"))
}

func verboseLabel(p paint, verbose bool) string {
	if !verbose {
		return p.dim(i18n.T("common.off"))
	}
	return p.green(i18n.T("common.on")) + p.dim(i18n.T("banner.verboseNote"))
}

// humanBytes renders a byte budget the way the portal shows it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KB", n>>10)
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// shortDuration trims Go's verbose durations ("10m0s" -> "10m").
func shortDuration(d time.Duration) string {
	if d == 0 {
		return i18n.T("common.unlimited")
	}
	text := d.Round(time.Second).String()
	text = strings.Replace(text, "m0s", "m", 1)
	text = strings.Replace(text, "h0m", "h", 1)
	return text
}

// prettyPath shortens the home directory to "~" so the banner stays narrow.
func prettyPath(path string) string {
	if path == "" {
		return "—"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
