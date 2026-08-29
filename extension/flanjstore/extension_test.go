package flanjstore

import (
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/extension"
)

// TestStoreHandleConcurrentWithStart pins the Start/Store synchronization:
// extension start order is user config, so another extension's goroutine (the
// UI's finding-sync ticker) may call Store() while Start is still writing the
// handle. Run under -race — an unguarded handle fails this test there. It also
// pins the nil-before-Start behaviour: a TRUE nil interface, never typed-nil.
func TestStoreHandleConcurrentWithStart(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = filepath.Join(t.TempDir(), "store.db")
	ext, err := create(context.Background(), extension.Settings{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := ext.(*storeExtension)

	if e.Store() != nil {
		t.Fatal("Store() before Start must be a true nil interface")
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = e.Store() // races Start's write without the guard
			}
		}
	}()

	if err := e.Start(context.Background(), nil); err != nil {
		close(stop)
		<-done
		t.Fatalf("Start: %v", err)
	}
	close(stop)
	<-done

	if e.Store() == nil {
		t.Fatal("Store() after Start must return the opened handle")
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
