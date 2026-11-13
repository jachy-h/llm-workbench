package i18n

import (
	"strings"
	"testing"
)

// TestCatalogIsComplete is the guard that matters: a message missing either
// language would print an empty line somewhere in the CLI or the page.
func TestCatalogIsComplete(t *testing.T) {
	// Machine tokens are deliberately identical in both languages: the portal
	// and the tests match on them.
	neutral := map[string]bool{"cli.logStart": true}

	if len(catalog) < 50 {
		t.Fatalf("catalog has only %d messages, did it get truncated?", len(catalog))
	}
	for key, message := range catalog {
		if strings.TrimSpace(message.ZH) == "" {
			t.Errorf("%s: missing ZH", key)
		}
		if strings.TrimSpace(message.EN) == "" {
			t.Errorf("%s: missing EN", key)
		}
		if message.ZH == message.EN && !neutral[key] && !strings.ContainsAny(message.ZH, "0123456789") {
			t.Errorf("%s: ZH and EN are identical (%q)", key, message.ZH)
		}
	}
}

func TestTFollowsCurrentLanguage(t *testing.T) {
	t.Cleanup(func() { Set(ZH) })

	Set(ZH)
	if got := T("common.none"); got != "（无）" {
		t.Errorf("ZH T = %q", got)
	}
	Set(EN)
	if got := T("common.none"); got != "(none)" {
		t.Errorf("EN T = %q", got)
	}
	if got := T("banner.statusBackground", 42); got != "background (pid 42)" {
		t.Errorf("EN T with args = %q", got)
	}
	// An unknown key falls back to the key, never to an empty string.
	if got := T("nope.missing"); got != "nope.missing" {
		t.Errorf("missing key = %q", got)
	}
}

func TestSetRejectsUnknownLanguage(t *testing.T) {
	t.Cleanup(func() { Set(ZH) })
	Set(Lang("fr"))
	if got := Current(); got != ZH {
		t.Errorf("Current after Set(fr) = %q, want zh", got)
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Lang{
		"zh":             ZH,
		"zh-CN":          ZH,
		"zh_CN.UTF-8":    ZH,
		"ZH":             ZH,
		" en ":           EN,
		"en_US.UTF-8":    EN,
		"en-GB":          EN,
		"en_US.UTF-8@eu": EN,
	}
	for in, want := range cases {
		got, ok := Parse(in)
		if !ok || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "auto", "C", "POSIX", "fr_FR.UTF-8"} {
		if got, ok := Parse(in); ok {
			t.Errorf("Parse(%q) = %q, true; want not recognised", in, got)
		}
	}
}

func TestDetectPrecedence(t *testing.T) {
	langs := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}
	}
	cases := []struct {
		name   string
		values map[string]string
		want   Lang
	}{
		{"nothing set", nil, ZH},
		{"ONP_LANG wins", map[string]string{"ONP_LANG": "en", "LANG": "zh_CN.UTF-8"}, EN},
		{"LC_ALL wins over LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, EN},
		{"LANG zh", map[string]string{"LANG": "zh_CN.UTF-8"}, ZH},
		{"LANG en", map[string]string{"LANG": "en_US.UTF-8"}, EN},
		{"C locale falls back to zh", map[string]string{"LANG": "C"}, ZH},
		{"auto defers to the locale", map[string]string{"ONP_LANG": "auto", "LANG": "en_US.UTF-8"}, EN},
	}
	for _, tc := range cases {
		if got := Detect(langs(tc.values)); got != tc.want {
			t.Errorf("%s: Detect = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Detect(nil); got != ZH {
		t.Errorf("Detect(nil) = %q, want zh", got)
	}
}
