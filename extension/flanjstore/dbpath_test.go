package flanjstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/extension"
)

// requireNonRoot skips the permission-dependent cases when the test runs as
// root, for whom 0555 is not a barrier. The does-not-exist and not-a-directory
// cases below have no such dependency and always run, so the diagnostic is
// never left entirely untested on a root CI runner.
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("running as root: mode bits do not deny root, so an unwritable directory cannot be staged")
	}
}

// unwritableDir returns a directory this process cannot create files in.
func unwritableDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	// t.TempDir's cleanup needs to descend into it.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if dirWritable(dir) {
		t.Skip("the filesystem ignores the mode bits here; cannot stage an unwritable directory")
	}
	return dir
}

// TestDiagnoseDBPathUnwritableDir is the /data case: the parent directory
// exists but this uid cannot create the database file in it. This is what a
// bare `docker run flanj-collector` hits, where the image's VOLUME ["/data"]
// becomes a root-owned anonymous volume and the process is uid 65532.
func TestDiagnoseDBPathUnwritableDir(t *testing.T) {
	requireNonRoot(t)
	dir := unwritableDir(t)

	got := diagnoseDBPath(filepath.Join(dir, "flanj.db"))
	if got == "" {
		t.Fatal("an unwritable directory must produce a diagnosis, not silence")
	}
	// The three things an operator needs: which directory, which identity, and
	// what to do — never just the driver's error code.
	for _, want := range []string{dir, "not writable", "uid ", "fsGroup", "docs/DEPLOYMENT.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnosis %q does not mention %q", got, want)
		}
	}
}

// TestDiagnoseDBPathMissingDir covers the other unactionable-(14) shape: the
// mount point is simply not there.
func TestDiagnoseDBPathMissingDir(t *testing.T) {
	got := diagnoseDBPath(filepath.Join(t.TempDir(), "absent", "flanj.db"))
	if got == "" {
		t.Fatal("a missing directory must produce a diagnosis")
	}
	for _, want := range []string{"does not exist", "docs/DEPLOYMENT.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnosis %q does not mention %q", got, want)
		}
	}
}

// TestDiagnoseDBPathNotADirectory covers db_path pointed under a regular file.
func TestDiagnoseDBPathNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := diagnoseDBPath(filepath.Join(file, "flanj.db"))
	if !strings.Contains(got, "is not a directory") {
		t.Errorf("diagnosis %q does not name the not-a-directory cause", got)
	}
}

// TestDiagnoseDBPathHealthy pins the negative: a perfectly good directory must
// add nothing. A diagnosis that fires on a healthy path would misattribute
// every other open failure — a corrupt file, a disk full — to the mount.
func TestDiagnoseDBPathHealthy(t *testing.T) {
	if got := diagnoseDBPath(filepath.Join(t.TempDir(), "flanj.db")); got != "" {
		t.Errorf("a writable directory must produce no diagnosis, got %q", got)
	}
	if got := diagnoseDBPath(""); got != "" {
		t.Errorf("an empty db_path is Validate's job, got %q", got)
	}
}

// TestStartUnwritableDirError is the end-to-end assertion: Start's error names
// the directory and the uid, and still wraps the driver's error underneath.
// Without the diagnosis the entire message is "flanjstore extension: open
// store: ping sqlite: unable to open database file (14)".
func TestStartUnwritableDirError(t *testing.T) {
	requireNonRoot(t)
	dir := unwritableDir(t)

	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = filepath.Join(dir, "flanj.db")
	ext, err := create(context.Background(), extension.Settings{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = ext.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("Start must fail when the store directory is unwritable")
	}
	msg := err.Error()
	for _, want := range []string{dir, "not writable", "fsGroup"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Start error %q does not mention %q", msg, want)
		}
	}
	// The driver's own error survives underneath, so nothing is lost.
	if !strings.Contains(msg, "unable to open database file") {
		t.Errorf("Start error %q dropped the underlying driver error", msg)
	}
}
