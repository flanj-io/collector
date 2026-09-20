package flanjstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/promote"
	"github.com/flanj-io/collector/internal/store"
)

const inboundMarker = "flanj.finding.inbound" // otlpattr.AttrFindingInbound

// newExporterOn starts an exporter over a given store.
func newExporterOn(t *testing.T, st store.Store) exporter.Logs {
	t.Helper()
	exp, err := createLogsExporter(context.Background(), testSettings(zap.NewNop()), fastRetry(t))
	if err != nil {
		t.Fatalf("create exporter: %v", err)
	}
	host := extHost{exts: map[component.ID]component.Component{
		component.MustNewID("flanjstore"): &storeProvider{st: st},
	}}
	if err := exp.Start(context.Background(), host); err != nil {
		t.Fatalf("start exporter: %v", err)
	}
	t.Cleanup(func() { _ = exp.Shutdown(context.Background()) })
	return exp
}

// storeBackends: sqlite always, postgres when FLANJ_TEST_PG_DSN names a scratch
// database (shared with other suites, so this test keys everything uniquely).
func storeBackends(t *testing.T) map[string]func(t *testing.T) store.Store {
	out := map[string]func(t *testing.T) store.Store{
		"sqlite": func(t *testing.T) store.Store {
			st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	}
	if dsn := os.Getenv("FLANJ_TEST_PG_DSN"); dsn != "" {
		out["postgres"] = func(t *testing.T) store.Store {
			st, err := store.OpenPostgres(dsn, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}
	}
	return out
}

// TestInboundMarker_StorePodPersistsItWithoutTheCall: a front's finding record
// for a self-spec finding carries the inbound marker, and its call never
// reaches the store (here: a record the store pod drops as not a call at
// all). The store pod persists inbound from the finding record alone, so the
// findings sync — and a flag built from the store — carry "self" and no
// service name anywhere. A later occurrence without the marker never clears it.
func TestInboundMarker_StorePodPersistsItWithoutTheCall(t *testing.T) {
	for name, open := range storeBackends(t) {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			exp := newExporterOn(t, st)
			service := fmt.Sprintf("orders-svc-sentinel-%d", time.Now().UnixNano())
			callID := otlpattr.NewID()
			f := model.Finding{SchemaVersion: model.SchemaVersion, ID: otlpattr.NewID(), Kind: model.KindLiveVsSpec,
				Severity: model.SeverityBreaking, Integration: service, Endpoint: "GET /v1/orders/{id}",
				FieldPath: model.Ptr("total"), Expected: "type=integer", Actual: "type=string", Rule: "type-mismatch",
				SourceCallID: &callID, DetectedAt: "2026-09-20T10:00:00.000Z"}
			f.Signature = f.ComputeSignature()

			send := func(fd model.Finding, marked bool) {
				t.Helper()
				ld := plog.NewLogs()
				rl := ld.ResourceLogs().AppendEmpty()
				rl.Resource().Attributes().PutStr("service.name", service)
				recs := rl.ScopeLogs().AppendEmpty().LogRecords()
				// The call: dropped at the store pod (no method, no route).
				c := recs.AppendEmpty()
				c.Attributes().PutStr(otlpattr.AttrCallID, callID)
				c.Attributes().PutStr(otlpattr.AttrDirection, "server")
				fr := recs.AppendEmpty()
				if err := otlpattr.FindingToRecord(fr, fd); err != nil {
					t.Fatal(err)
				}
				if marked {
					fr.Attributes().PutBool(inboundMarker, true)
				}
				if err := exp.ConsumeLogs(context.Background(), ld); err != nil {
					t.Fatalf("ConsumeLogs: %v", err)
				}
			}
			send(f, true)
			waitFor(t, "the finding stored", func() bool {
				_, ok, _ := st.GetFinding(f.ID)
				return ok
			})
			if _, ok, _ := st.GetCall(callID); ok {
				t.Fatal("the test needs the call NOT stored")
			}
			inbound, err := st.InboundFindingIDs()
			if err != nil {
				t.Fatal(err)
			}
			if !inbound[f.ID] {
				t.Errorf("store pod did not persist inbound from the finding record: %v", inbound)
			}

			// The findings sync.
			findings, err := st.ListFindings(200)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(promote.FindingsRequest{Findings: promote.BuildFindingShapes(findings, inbound)})
			if strings.Contains(string(wire), service) {
				t.Errorf("the findings sync carries the service name: %s", wire)
			}
			// A flag built from the store.
			stored, _, _ := st.GetFinding(f.ID)
			req := promote.Build(promote.Input{Finding: stored, Inbound: inbound[f.ID]})
			flag, _ := json.Marshal(req)
			if strings.Contains(string(flag), service) || req.Finding.Integration != "self" {
				t.Errorf("the flag request carries the service name or not self: %s", flag)
			}

			// Never cleared: the same drift again, from a record without it.
			again := f
			again.ID = otlpattr.NewID()
			send(again, false)
			waitFor(t, "the second occurrence", func() bool {
				got, _, _ := st.GetFinding(f.ID)
				return got.OccurrenceCount >= 2
			})
			if inbound, _ = st.InboundFindingIDs(); !inbound[f.ID] {
				t.Errorf("a later unmarked occurrence cleared inbound: %v", inbound)
			}
		})
	}
}
