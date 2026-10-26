// Package microserver embeds the built dashboard (web/dist) into the binary.
package microserver

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var webDist embed.FS

// WebFS is the dashboard's file tree, rooted at web/dist.
func WebFS() fs.FS {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		panic(err)
	}
	return sub
}
