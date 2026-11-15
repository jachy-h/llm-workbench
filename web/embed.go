// Package web embeds the built workbench frontend.
package web

import "embed"

// Dist is the Vite build output. It is committed so a plain `go build` works
// without Node; CI rebuilds it and verifies the tree is clean.
//
//go:embed all:dist
var Dist embed.FS
