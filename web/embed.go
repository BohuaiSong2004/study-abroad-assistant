// Package web embeds the browser UI (HTML5, CSS3, jQuery).
package web

import "embed"

// Static is the UI served by the gateway.
//
//go:embed all:static
var Static embed.FS
