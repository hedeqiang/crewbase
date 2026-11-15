package google

import (
	"embed"
	"strings"
)

// The OAuth client secret written at build time (see DefaultClientSecret):
// client_secret.txt is git-ignored; client_secret.example.txt is committed
// (empty) so the embed pattern always matches and builds without the
// secret still compile.
//
//go:embed client_secret*.txt
var secretFiles embed.FS

var embeddedSecret = func() string {
	b, err := secretFiles.ReadFile("client_secret.txt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}()
