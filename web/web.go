// Package web holds the embedded frontend: the static page assets and the
// locale files. It exists as its own package so the Go sources can live in
// internal/ while go:embed paths stay relative to this directory.
package web

import "embed"

//go:embed static locales
var FS embed.FS
