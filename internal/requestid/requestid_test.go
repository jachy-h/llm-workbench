package requestid

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := New()
		if !strings.HasPrefix(id, "req_") {
			t.Fatalf("id = %q", id)
		}
		if len(id) != len("req_")+26 {
			t.Errorf("id length = %d (%q)", len(id), id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
