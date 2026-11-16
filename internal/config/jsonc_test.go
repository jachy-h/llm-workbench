package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string // JSON-decoded equality target, not byte equality
	}{
		"line comment": {
			in: `{
  // note
  "a": 1
}`,
			want: `{"a":1}`,
		},
		"block comment": {
			in:   `{"a": /* inline */ 1}`,
			want: `{"a":1}`,
		},
		"comment markers inside a string": {
			// http:// is not a comment, and neither is /* inside a value.
			in:   `{"upstream": "http://127.0.0.1:11434", "x": "a/*b*/c"}`,
			want: `{"upstream":"http://127.0.0.1:11434","x":"a/*b*/c"}`,
		},
		"escaped quote before a comment": {
			in:   `{"a": "say \"//\" ok" // then a real comment` + "\n}",
			want: `{"a":"say \"//\" ok"}`,
		},
		"trailing commas": {
			in: `{
  "a": [1, 2, ],
  "b": {"c": 3, },
}`,
			want: `{"a":[1,2],"b":{"c":3}}`,
		},
		"comma before a comment then a brace": {
			in:   `{"a": 1, /* last */}`,
			want: `{"a":1}`,
		},
		"plain json is untouched": {
			in:   `{"a":[1,2],"b":"x"}`,
			want: `{"a":[1,2],"b":"x"}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got, want any
			stripped := stripJSONC([]byte(tc.in))
			if err := json.Unmarshal(stripped, &got); err != nil {
				t.Fatalf("stripped output is not JSON (%v): %q", err, stripped)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatalf("bad expectation: %v", err)
			}
			if !jsonEqual(got, want) {
				t.Errorf("stripJSONC(%q) = %q, want %s", tc.in, stripped, tc.want)
			}
		})
	}
}

// TestStripJSONCKeepsLineNumbers guards the error messages: a broken config
// should point at the line the user actually wrote.
func TestStripJSONCKeepsLineNumbers(t *testing.T) {
	in := "{\n  // one\n  /* two\n     three */\n  \"a\": 1,\n  \"b\": ,\n}"
	var document any
	err := json.Unmarshal(stripJSONC([]byte(in)), &document)
	syntax, ok := err.(*json.SyntaxError)
	if !ok {
		t.Fatalf("want a syntax error, got %v", err)
	}
	if line := 1 + strings.Count(in[:syntax.Offset], "\n"); line != 6 {
		t.Errorf("error line = %d, want 6", line)
	}
}

func jsonEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}
