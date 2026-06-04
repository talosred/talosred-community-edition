package dashboard

import "embed"

//go:embed templates
var tmplFS embed.FS

//go:embed static
var staticFS embed.FS
