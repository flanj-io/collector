package flanjstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// diagnoseDBPath explains, in operator terms, why the SQLite file at path could
// not be opened — or returns "" when the directory looks fine and the driver's
// own error is the only thing there is to say.
//
// It exists because the driver's error is unactionable. The image declares
// VOLUME ["/data"] and runs as distroless nonroot (uid 65532), so a bare
// `docker run` with no -v gets an anonymous volume created root-owned, the
// store cannot create /data/flanj.db, and the whole diagnosis a stranger is
// handed is "ping sqlite: unable to open database file (14)". Nothing in that
// sentence says "mount" or "ownership". The checks below name the directory,
// the uid that cannot write it, and what to do about it.
//
// Writability is PROBED, not computed from the mode bits: supplementary
// groups, an fsGroup, a read-only mount and ACLs all decide the outcome, and
// only an actual create attempt sees all four.
func diagnoseDBPath(path string) string {
	if path == "" {
		return "" // Config.Validate already refuses this.
	}
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("db_path %q: directory %s does not exist — create it (a volume mount, a PVC, or mkdir -p) and make it writable by %s; see docs/DEPLOYMENT.md", path, dir, self())
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("db_path %q: directory %s cannot even be examined by %s — a parent directory denies it; see docs/DEPLOYMENT.md", path, dir, self())
	case err != nil:
		// Stat failed for a reason we cannot name better than the driver can.
		return ""
	case !fi.IsDir():
		return fmt.Sprintf("db_path %q: %s is not a directory", path, dir)
	case !dirWritable(dir):
		return fmt.Sprintf("db_path %q: directory %s is not writable by %s (%s) — mount a writable volume there, pre-chown it to that uid, or set a pod fsGroup; see docs/DEPLOYMENT.md", path, dir, self(), describe(fi))
	}
	return ""
}

// dirWritable reports whether this process can create a file in dir.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".flanjstore-writecheck-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// self names the identity the collector is running under — the number an
// operator puts in a chown or an fsGroup.
func self() string {
	return fmt.Sprintf("uid %d gid %d", os.Getuid(), os.Getgid())
}

// describe names who does own the directory, so the mismatch with self() is on
// the same line as the complaint.
func describe(fi fs.FileInfo) string {
	return owner(fi) + "mode " + fi.Mode().String()
}
