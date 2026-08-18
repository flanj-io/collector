package viniferaui

import "embed"

// webDist holds the built Vue SPA (ui/dist), copied into web/dist by the
// Dockerfile's node stage before the Go build. A committed placeholder keeps the
// package compilable in local dev before the UI is built. `all:` includes files
// whose names start with `.` or `_` (Vite emits some under assets/).
//
//go:embed all:web/dist
var webDist embed.FS
