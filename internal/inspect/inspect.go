// Package inspect contains the neutral capture record shared by the proxy
// and the inspector. Keeping it dependency-free means the transparent proxy
// does not import SQLite or the workbench.
package inspect

import "time"

// Capture is one completed proxied call.
type Capture struct {
	ID                string
	Started           time.Time
	Source            string
	Provider          string
	Method            string
	Path              string
	Client            string
	Note              string
	Stream            bool
	Status            int
	Error             string
	Duration          time.Duration
	TTFT              time.Duration
	RequestOriginal   []byte
	RequestForwarded  []byte
	ResponseBody      []byte
	ResponseTruncated bool
	// CaptureBodies overrides the inspector's global body setting for this
	// capture (playground/experiments record their own bodies).
	CaptureBodies bool
}

// Recorder receives completed captures. Implementations must not block the
// request path; queue and write asynchronously.
type Recorder interface {
	Record(Capture)
}
