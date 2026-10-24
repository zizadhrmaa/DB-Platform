// Package web meng-embed template dan file statis ke dalam binary.
package web

import "embed"

//go:embed templates static
var FS embed.FS
