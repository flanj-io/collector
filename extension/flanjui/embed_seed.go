package flanjui

import _ "embed"

// directorySeedRaw is the curated directory seed baked into the collector image
// (v1 phase 1, ruling 5): registrable domain → { name, tier }. It renders on
// first paint, offline, pre-Connect; once Connected, a periodically pulled full
// table (KV `directory.table`) merges OVER it — see directory.go. The file is
// committed in this public repo deliberately (a trust asset).
//
//go:embed directory-seed.json
var directorySeedRaw []byte
