package flanjui

import (
	"context"
	"testing"
	"time"
)

// TestConfiguredButUnconnectedMakesNoOutboundRequest: the image now ships with
// cp_base_url set to the hosted control plane, so a first `docker run` has a
// CP client and NO collector key. That state must be silent on the wire —
// the sync ticker's three legs (findings, directory, edges) and the whole
// loop are gated on the key, and setting the URL only constructs the client.
// Counted at the stub's front door, on every route, so a leg that grew a
// key-less call would be caught whatever path it took.
func TestConfiguredButUnconnectedMakesNoOutboundRequest(t *testing.T) {
	r := newRig(t)
	r.start(t) // e.cp is built; the store holds findings and a call, and NO key
	// Something for every leg to send if it forgot the gate.
	_ = r.st.InsertFinding(syncSentinelFinding("fnd_unconnected", "amount"))

	// Each leg directly, as one tick would run them.
	r.ext.syncFindingsOnce(context.Background())
	r.ext.syncDirectoryOnce(context.Background())
	r.ext.syncEdgesOnce(context.Background())
	if n := r.cp.requestCount(); n != 0 {
		t.Fatalf("unconnected collector reached the control plane %d time(s) from the sync legs", n)
	}

	// The real loop: startFindingSync fires its first tick immediately.
	r.ext.cfg.FindingSync, r.ext.cfg.DirectorySync, r.ext.cfg.EdgeSync = true, true, true
	r.ext.startFindingSync()
	if r.ext.syncDone == nil {
		t.Fatal("sync loop did not start — the test proves nothing")
	}
	time.Sleep(200 * time.Millisecond)
	r.ext.stopFindingSync()
	if n := r.cp.requestCount(); n != 0 {
		t.Fatalf("unconnected collector reached the control plane %d time(s) from the sync loop", n)
	}

	// A configured CP is reported, so the UI shows the Connect form.
	_, body, _ := r.do(t, "GET", "/api/health", nil)
	if body["cp_configured"] != true {
		t.Fatalf("cp_configured = %v, want true with cp_base_url set", body["cp_configured"])
	}
	if body["connect_status"] != "disconnected" {
		t.Fatalf("connect_status = %v, want disconnected with no key", body["connect_status"])
	}
}
