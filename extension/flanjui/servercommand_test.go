package flanjui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// launchLine is a stdio server's flanj.mcp.server.command as the store holds
// it; the package name is a canary no other field in these fixtures carries.
const launchLine = `["npx","-y","@canary/launch-line-7f3a@0.2.1"]`

func stdioContract(command string) model.SpecInfo {
	return model.SpecInfo{
		Integration: "acme-stdio", Role: model.SpecRoleProvider, PeerHost: "acme-stdio-mcp",
		EdgeClass: model.EdgeClassLocalProcess, Format: model.SpecFormatMCP, Source: model.SpecSourceObserved,
		Title: "acme-stdio-mcp", Version: "0.2.1", Endpoints: 1, LoadedAt: "2026-09-18T10:00:00Z",
		ServerCommand: command,
	}
}

// TestContractsServeServerCommand: GET /api/contracts — the rows the Contracts
// tab renders — carries `server_command` as the JSON array STRING the store
// holds, read through a REAL store (the column, the list query, the handler),
// and omits the key on a row that has none: additive, for tolerant readers.
func TestContractsServeServerCommand(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flanj.db"), 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutMCPCatalogue(stdioContract(launchLine), []byte(`{"tools":[]}`)); err != nil {
		t.Fatalf("put stdio: %v", err)
	}
	remote := stdioContract("")
	remote.Integration, remote.PeerHost, remote.EdgeClass = "acme-tools", "mcp.acme.test", model.EdgeClassExternal
	if err := st.PutMCPCatalogue(remote, []byte(`{"tools":[]}`)); err != nil {
		t.Fatalf("put remote: %v", err)
	}

	ext := &uiExtension{
		cfg:       &Config{UIEndpoint: "127.0.0.1:0"},
		telemetry: component.TelemetrySettings{Logger: zap.NewNop()},
		st:        st,
	}
	srv := httptest.NewServer(ext.routes())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/contracts")
	if err != nil {
		t.Fatalf("GET /api/contracts: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Contracts []map[string]any `json:"contracts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/contracts = %d, decode err %v", resp.StatusCode, err)
	}
	byID := map[string]map[string]any{}
	for _, c := range out.Contracts {
		id, _ := c["integration"].(string)
		byID[id] = c
	}
	if got := byID["acme-stdio"]["server_command"]; got != launchLine {
		t.Errorf("stdio row server_command = %#v, want the JSON array string %q", got, launchLine)
	}
	if v, has := byID["acme-tools"]["server_command"]; has {
		t.Errorf("a row without a launch line must omit the key, got %#v", v)
	}
}

// TestFlagNeverCarriesServerCommand: the launch line is LOCAL DISPLAY. A flag
// raised on a stdio server's finding — its contract row holding the command —
// sends the CP the call and the finding and nothing from that row: the canary
// package name and the field name appear nowhere in the body.
func TestFlagNeverCarriesServerCommand(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = saveConnect(r.st, connectState{CollectorKey: r.cp.collectorKey, ConsumerDisplayName: "Acme",
		ContactEmail: "ops@acme.test", ContactStatus: "confirmed", ConfirmedContactEmail: "ops@acme.test"})
	r.cp.mu.Lock()
	r.cp.contactEmail, r.cp.contactStatus, r.cp.confirmedEmail = "ops@acme.test", "confirmed", "ops@acme.test"
	r.cp.mu.Unlock()

	_ = r.st.PutMCPCatalogue(stdioContract(launchLine), []byte(`{"tools":[]}`))
	callID := "call_stdio_1"
	_ = r.st.InsertCall(model.RedactedCall{SchemaVersion: 1, ID: callID, CapturedAt: "2026-09-18T10:00:00Z",
		Integration: "acme-stdio", Direction: "client", PeerHost: "acme-stdio-mcp", EdgeClass: model.EdgeClassLocalProcess,
		Method: "tools/call", Route: "/get_balance", URL: "mcp://acme-stdio-mcp/get_balance",
		RequestBody: "{}", ResponseBody: `{"amount":"1200"}`, Transport: "mcp", MCPToolName: "get_balance",
		Redaction: model.Redaction{Patterns: []string{}}})
	_ = r.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_stdio", Kind: model.KindOutputMismatch,
		Severity: model.SeverityBreaking, Integration: "acme-stdio", Endpoint: "get_balance",
		Expected: "type=integer", Actual: `type=string ("1200")`, Rule: "type-mismatch",
		SourceCallID: &callID, DetectedAt: "2026-09-18T10:00:01Z"})

	resp, out, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_stdio", "allowed_domains": nil, "allowed_emails": nil})
	if resp.StatusCode != http.StatusCreated || out["thread_url"] == "" {
		t.Fatalf("flag = %d %s", resp.StatusCode, raw)
	}
	body, err := json.Marshal(r.cp.lastFlagBody)
	if err != nil || len(r.cp.lastFlagBody) == 0 {
		t.Fatalf("no flag body reached the CP: %v", err)
	}
	for _, leak := range []string{"launch-line-7f3a", "@canary/", "server_command", "npx"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the flag body carries %q — the launch line is local display only:\n%s", leak, body)
		}
	}
}
