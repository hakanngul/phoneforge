// Package frontend embeds the PhoneForge web UI so the server and the desktop
// app can serve it without a frontend/ directory on disk.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed *.html *.js *.css
var files embed.FS

// FS returns the embedded UI rooted at the frontend directory.
func FS() fs.FS { return files }
