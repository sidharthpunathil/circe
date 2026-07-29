// Package web embeds the HTML templates and static assets so the server ships
// as a single self-contained binary with no runtime file dependencies.
package web

import "embed"

//go:embed all:templates
var Templates embed.FS

//go:embed all:static
var Static embed.FS
