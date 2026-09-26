package api

import (
	"embed"
	"io/fs"
)

// webFiles embeds the dashboard so the image ships with no build step and no
// external assets (the container may have no internet access).
//
//go:embed all:web/out
var webFiles embed.FS

// webAssets returns the dashboard files rooted at the web directory.
func webAssets() fs.FS {
	sub, err := fs.Sub(webFiles, "web/out")
	if err != nil {
		panic(err)
	}
	return sub
}
