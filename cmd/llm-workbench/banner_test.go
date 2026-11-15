package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jachy-h/llm-workbench/internal/config"
	"github.com/jachy-h/llm-workbench/internal/i18n"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		512:           "512 B",
		2048:          "2 KB",
		1536:          "1.5 KB",
		1 << 20:       "1 MB",
		(3 << 20) / 2: "1.5 MB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "无限制",
		10 * time.Minute:        "10m",
		time.Hour:               "1h",
		90 * time.Second:        "1m30s",
		1500 * time.Millisecond: "2s",
	}
	for in, want := range cases {
		if got := shortDuration(in); got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayWidthAndPad(t *testing.T) {
	if got := displayWidth("listen"); got != 6 {
		t.Errorf("displayWidth(listen) = %d, want 6", got)
	}
	if got := displayWidth("改参数、看日志"); got != 14 {
		t.Errorf("displayWidth(CJK) = %d, want 14", got)
	}
	if got := pad("listen", 10); got != "listen    " {
		t.Errorf("pad(listen,10) = %q", got)
	}
	// CJK labels must still land the values in the same column.
	if displayWidth(pad("停止服务", 15)) != 15 {
		t.Errorf("pad(停止服务,15) column = %d, want 15", displayWidth(pad("停止服务", 15)))
	}
}

func TestPrettyPath(t *testing.T) {
	t.Setenv("HOME", "/Users/tester")
	cases := map[string]string{
		"/Users/tester/Library/config.json": "~/Library/config.json",
		"/Users/tester":                     "~",
		"/tmp/proxy.log":                    "/tmp/proxy.log",
		"":                                  "—",
	}
	for in, want := range cases {
		if got := prettyPath(in); got != want {
			t.Errorf("prettyPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBannerIsPlainWhenNotATerminal(t *testing.T) {
	cfg, err := config.Parse([]string{"--port", "11434", "--no-browser"}, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var out bytes.Buffer
	printBanner(&out, cfg, "http://127.0.0.1:11434/__portal", bannerOptions{
		Status: "后台运行 (pid 42)",
		Stop:   "portal 页面上的「关闭服务」",
	})
	text := out.String()

	if strings.Contains(text, "\x1b") {
		t.Errorf("banner contains ANSI escapes on a non-terminal writer:\n%q", text)
	}
	for _, needle := range []string{
		"llm-workbench",
		"后台运行 (pid 42)",
		"listen    http://127.0.0.1:11434",
		"portal    http://127.0.0.1:11434/__portal",
		"inject",
		"强制关闭思考",
		"停止服务",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("banner missing %q:\n%s", needle, text)
		}
	}
}

func TestPaintDisabledByNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if newPaint(&bytes.Buffer{}).color {
		t.Error("paint enabled for a buffer writer")
	}
}

func TestBannerInEnglish(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })

	cfg, err := config.Parse([]string{"--port", "11434", "--no-browser", "--lang", "en"},
		func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var out bytes.Buffer
	printBanner(&out, cfg, "http://127.0.0.1:11434/__portal", bannerOptions{
		Status: i18n.T("banner.statusBackground", 42),
		Stop:   i18n.T("banner.stopPortalOrKill", 42),
	})
	text := out.String()

	for _, needle := range []string{
		"background (pid 42)",
		"concurrency 8",
		"+ thinking forced off",
		"settings & logs",
		"verbose   off",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("English banner missing %q:\n%s", needle, text)
		}
	}
	for _, unwanted := range []string{"后台运行", "强制关闭思考", "改参数、看日志", "关闭"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("English banner still contains %q:\n%s", unwanted, text)
		}
	}
	// The labels share a column in both languages.
	if !strings.Contains(text, "listen    http://127.0.0.1:11434") {
		t.Errorf("English banner lost the label column:\n%s", text)
	}
}

func TestBannerReflectsThinkingMode(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	cases := map[string]string{
		"on":          "不再强制关闭思考",
		"passthrough": "思考原样透传",
	}
	for mode, needle := range cases {
		cfg, err := config.Parse([]string{"--port", "11434", "--no-browser", "--thinking", mode},
			func(string) (string, bool) { return "", false })
		if err != nil {
			t.Fatalf("parse %s: %v", mode, err)
		}
		var out bytes.Buffer
		printBanner(&out, cfg, "http://127.0.0.1:11434/__portal", bannerOptions{})
		if !strings.Contains(out.String(), needle) {
			t.Errorf("banner for %s missing %q:\n%s", mode, needle, out.String())
		}
	}
}
