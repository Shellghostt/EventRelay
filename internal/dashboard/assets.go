// Package dashboard embeds reproducibly built React assets for Go-only deployments.
package dashboard

import "embed"

// Assets is generated from web/ by make frontend-build.
//
//go:embed dist
var Assets embed.FS
