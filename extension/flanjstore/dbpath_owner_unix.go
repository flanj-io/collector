//go:build unix

package flanjstore

import (
	"fmt"
	"io/fs"
	"syscall"
)

// owner renders the directory's owning uid/gid, with a trailing separator, or
// "" when the platform does not report them.
func owner(fi fs.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("owner uid %d gid %d, ", st.Uid, st.Gid)
}
