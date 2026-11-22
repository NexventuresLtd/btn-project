// Package assets embeds fonts and default logos.
package assets

import "embed"

//go:embed fonts/*.ttf *.png
var FS embed.FS
