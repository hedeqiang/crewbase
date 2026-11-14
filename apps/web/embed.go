// Package web embeds the built desktop UI (apps/web/dist). Run `make web`
// (pnpm build) before building crewd to include it; without a build only
// dist/.keep is embedded and Available reports false.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the UI file system rooted at dist/ (index.html at its root).
var Dist fs.FS

func init() {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	Dist = sub
}

// Available reports whether a built UI (dist/index.html) is embedded.
func Available() bool {
	_, err := fs.Stat(Dist, "index.html")
	return err == nil
}
