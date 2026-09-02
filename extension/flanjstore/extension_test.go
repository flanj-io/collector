package flanjstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

// TestSpecChangeReachesEverySubscriber: the store extension is the rendezvous
// between the component that CHANGES the contract set (the UI's upload/remove
// handlers) and the ones that CACHE it (the drift processor). Every registered
// listener must hear every announcement — a missed one puts an upload back on
// the refresh ticker, which is the wait the announcement exists to remove.
func TestSpecChangeReachesEverySubscriber(t *testing.T) {
	e := &storeExtension{cfg: createDefaultConfig().(*Config)}

	var got []string
	e.OnSpecsChanged(func() { got = append(got, "a") })
	e.OnSpecsChanged(func() { got = append(got, "b") })
	e.OnSpecsChanged(nil) // must not panic, and must not register

	e.NotifySpecsChanged()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("subscribers called = %v, want [a b]", got)
	}

	e.NotifySpecsChanged()
	if len(got) != 4 {
		t.Fatalf("a subscriber is one-shot: calls = %d, want 4 after two announcements", len(got))
	}
}

// TestSpecChangeWithNoSubscribersIsANoOp: a store pod of the tiered topology
// runs no drift processor, so nothing subscribes. An upload there must not
// depend on a listener existing.
func TestSpecChangeWithNoSubscribersIsANoOp(t *testing.T) {
	e := &storeExtension{cfg: createDefaultConfig().(*Config)}
	e.NotifySpecsChanged()
}

// TestSpecChangeDoesNotHoldTheRegistryLock: a subscriber is arbitrary code. If
// the announcement held the registry lock across it, one listener that
// registers (or announces) from its own callback would deadlock every future
// change. Snapshot-then-call is what makes that impossible.
func TestSpecChangeDoesNotHoldTheRegistryLock(t *testing.T) {
	e := &storeExtension{cfg: createDefaultConfig().(*Config)}

	reentered := false
	e.OnSpecsChanged(func() {
		e.OnSpecsChanged(func() { reentered = true })
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.NotifySpecsChanged() // registers the second subscriber
		e.NotifySpecsChanged() // ... which now runs
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("announcing deadlocked against a subscriber that touched the registry")
	}
	if !reentered {
		t.Error("a subscriber registered from a callback never ran")
	}
}
