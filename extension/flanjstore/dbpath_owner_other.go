//go:build !unix

package flanjstore

import "io/fs"

// owner reports nothing off unix — the mode string in describe() carries what
// there is to say.
func owner(fs.FileInfo) string { return "" }
