// Package web embeds the tablet UI so the compiled binary is fully
// self-contained - no static files need to ship separately in the Docker
// image.
package web

import "embed"

//go:embed static
var StaticFS embed.FS
