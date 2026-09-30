// Package jsclient embeds the JavaScript client's sources, so the functions
// runner can give each function a real client as ctx.db. The client itself
// is plain ESM JavaScript with no dependencies (src/).
package jsclient

import "embed"

// Sources holds src/*.js.
//
//go:embed src/*.js
var Sources embed.FS
