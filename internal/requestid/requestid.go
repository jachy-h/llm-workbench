// Package requestid generates short, sortable-enough identifiers for model
// calls. They are local-only correlation IDs; no meaning is encoded in them.
package requestid

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

const DefaultPrefix = "req_"

// New returns a fresh request ID such as req_3f6k2m....
func New() string { return NewWithPrefix(DefaultPrefix) }

// NewWithPrefix returns an ID with a custom prefix, e.g. prm_ for prompts.
func NewWithPrefix(prefix string) string {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failing is catastrophic; a fixed-but-unique fallback
		// would hide it. Panicking is better than silently duplicating IDs.
		panic("requestid: crypto/rand failed: " + err.Error())
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	return prefix + strings.ToLower(encoded)
}
