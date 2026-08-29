package diff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/flanj-io/collector/contract"
	"github.com/flanj-io/collector/contract/openapi"
)

// diffCases mirrors contracts/contract-diff-cases.json.
type diffCases struct {
	Cases []struct {
		Name   string          `json:"name"`
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
		Expect []struct {
			Class       Class  `json:"class"`
			OperationID string `json:"operationId"`
			Rule        string `json:"rule"`
			FieldPath   string `json:"fieldPath"`
			Before      any    `json:"before"`
			After       any    `json:"after"`
		} `json:"expect"`
	} `json:"cases"`
}

func loadCases(t *testing.T) diffCases {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "contract-diff-cases.json"))
	if err != nil {
		t.Fatalf("read battery: %v", err)
	}
	var cs diffCases
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("parse battery: %v", err)
	}
	if len(cs.Cases) == 0 {
		t.Fatal("battery is empty")
	}
	return cs
}

func toContract(t *testing.T, raw json.RawMessage, observedAt string) *contract.Contract {
	t.Helper()
	tools, err := contract.ParseToolsList(raw)
	if err != nil {
		t.Fatalf("parse tools list: %v", err)
	}
	c, err := contract.FromToolsList(tools, "mcp.provider.test|outbound", observedAt, "observed tools/list at "+observedAt)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return c
}

type key struct {
	Class       Class
	OperationID string
	Rule        string
	FieldPath   string
}

func sortKeys(ks []key) {
	sort.Slice(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		if a.OperationID != b.OperationID {
			return a.OperationID < b.OperationID
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.FieldPath != b.FieldPath {
			return a.FieldPath < b.FieldPath
		}
		return a.Class < b.Class
	})
}

// TestClassify_FixtureBattery is the Step A classifier acceptance battery
// (spec §4.A accept (2)): >=2 fixture cases per class, including a rename.
func TestClassify_FixtureBattery(t *testing.T) {
	cs := loadCases(t)
	perClass := map[Class]int{}
	sawRename := false

	for _, tc := range cs.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			before := toContract(t, tc.Before, "2026-08-23T00:00:00Z")
			after := toContract(t, tc.After, "2026-08-24T00:00:00Z")
			got := Classify(before, after)

			gotKeys := make([]key, 0, len(got))
			for _, c := range got {
				gotKeys = append(gotKeys, key{c.Class, c.OperationID, c.Rule, c.FieldPath})
			}
			wantKeys := make([]key, 0, len(tc.Expect))
			for _, e := range tc.Expect {
				wantKeys = append(wantKeys, key{e.Class, e.OperationID, e.Rule, e.FieldPath})
			}
			sortKeys(gotKeys)
			sortKeys(wantKeys)
			if len(gotKeys) != len(wantKeys) {
				t.Fatalf("change count: got %d want %d\n got:  %+v\n want: %+v", len(gotKeys), len(wantKeys), gotKeys, wantKeys)
			}
			for i := range wantKeys {
				if gotKeys[i] != wantKeys[i] {
					t.Errorf("change %d: got %+v want %+v", i, gotKeys[i], wantKeys[i])
				}
			}

			for _, c := range got {
				perClass[c.Class]++
				if c.Rule == RuleOperationRenamed {
					sawRename = true
					if c.Before != "create_refund" || c.After != "refund_create" {
						t.Errorf("rename before/after: got %v -> %v", c.Before, c.After)
					}
				}
				// Fragments, not whole schemas: every schema-level change must
				// carry a before and/or after fragment.
				switch c.Rule {
				case RuleInputTypeChanged, RuleOutputPropertyTypeChanged,
					RuleInputEnumValueRemoved, RuleInputEnumValueAdded,
					RuleOutputEnumValueRemoved, RuleOutputEnumValueAdded:
					if c.Before == nil || c.After == nil {
						t.Errorf("%s at %s: missing before/after fragment", c.Rule, c.FieldPath)
					}
				case RuleInputPropertyRemoved, RuleOutputRequiredPropertyRemoved:
					if c.Before == nil {
						t.Errorf("%s at %s: missing before fragment", c.Rule, c.FieldPath)
					}
				case RuleInputRequiredPropertyAdded, RuleInputOptionalPropertyAdded, RuleOutputOptionalPropertyAdded:
					if c.After == nil {
						t.Errorf("%s at %s: missing after fragment", c.Rule, c.FieldPath)
					}
				}
			}
		})
	}

	for _, cls := range []Class{ClassBreaking, ClassNonBreaking, ClassDescription} {
		if perClass[cls] < 2 {
			t.Errorf("battery covers class %s only %d time(s); spec requires >=2 cases", cls, perClass[cls])
		}
	}
	if !sawRename {
		t.Error("battery has no rename case; spec requires one")
	}
}

// TestClassify_TransportNeutral replays a change through the OPENAPI loader:
// the classifier must produce the same classification when the same logical
// change arrives via OpenAPI documents instead of tools/list snapshots.
func TestClassify_TransportNeutral(t *testing.T) {
	mk := func(amountType string) *contract.Contract {
		doc, err := openapi.LoadData([]byte(`
openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /balance:
    get:
      description: Get the balance.
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  amount: {type: ` + amountType + `}
                required: [amount]
`))
		if err != nil {
			t.Fatalf("load spec: %v", err)
		}
		c, err := openapi.From(doc, "api.provider.test|outbound", "2026-08-24T00:00:00Z", "local file inline")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		return c
	}
	got := Classify(mk("number"), mk("string"))
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Class != ClassBreaking || c.Rule != RuleOutputPropertyTypeChanged ||
		c.OperationID != "GET /balance" || c.FieldPath != "output.amount" {
		t.Errorf("unexpected change: %+v", c)
	}
}

// TestClassify_Idempotent: diffing a contract against itself yields nothing.
func TestClassify_Idempotent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "contract-normalization-tools-list.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tools, err := contract.ParseToolsList(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c1, err := contract.FromToolsList(tools, "e", "t1", "p")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	c2, err := contract.FromToolsList(tools, "e", "t2", "p")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := Classify(c1, c2); len(got) != 0 {
		t.Errorf("self-diff produced changes: %+v", got)
	}
}
