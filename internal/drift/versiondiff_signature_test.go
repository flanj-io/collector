package drift

import (
	"strings"
	"testing"
)

// The same document at two versions, differing ONLY in that two separate
// response properties each lose an enum value. One endpoint, one rule, two
// changes — the shape the store's per-signature dedup collapses when nothing
// distinguishes the two.
const vdSpecV1 = `
openapi: 3.0.0
info: { title: Acme, version: 1.0.0 }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  status:   { type: string, enum: [active, pending, closed] }
                  currency: { type: string, enum: [usd, eur, gbp] }
`

const vdSpecV2 = `
openapi: 3.0.0
info: { title: Acme, version: 2.0.0 }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  status:   { type: string, enum: [active, closed] }
                  currency: { type: string, enum: [usd, gbp] }
`

// TestTwoPropertiesUnderOneRuleAreTwoFindings.
//
// The signature is `integration|endpoint|kind|rule|field_path` (CONTRACTS §4)
// and the store's unique index dedups on it. version-diff left field_path nil,
// so two properties breaking under ONE rule on ONE endpoint produced two
// findings with ONE signature: the upload counted both and the store kept one.
// That is the arithmetic behind the reported "notice said 4, the API held 2".
func TestTwoPropertiesUnderOneRuleAreTwoFindings(t *testing.T) {
	findings, err := DetectVersionDiffData([]byte(vdSpecV1), []byte(vdSpecV2), "acme-payments")
	if err != nil {
		t.Fatalf("version diff: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2 (one enum value removed from each of two properties)", len(findings))
	}

	// Same endpoint and same rule — that is the premise, so assert it rather
	// than assuming oasdiff kept classifying them together.
	if findings[0].Endpoint != findings[1].Endpoint {
		t.Fatalf("the two changes are on different endpoints (%q, %q) — this test no longer covers the collapse",
			findings[0].Endpoint, findings[1].Endpoint)
	}
	if findings[0].Rule != findings[1].Rule {
		t.Fatalf("the two changes fired different rules (%q, %q) — this test no longer covers the collapse",
			findings[0].Rule, findings[1].Rule)
	}

	if findings[0].Signature == findings[1].Signature {
		t.Errorf("both changes share signature %q — the store keeps one row and the upload's count is a lie",
			findings[0].Signature)
	}
	// field_path is what discriminates them, and it has to name the property
	// each change is about.
	for _, f := range findings {
		if f.FieldPath == nil || *f.FieldPath == "" {
			t.Fatalf("finding %s has no field_path — nothing distinguishes it inside its rule", f.Rule)
		}
	}
	byProperty := map[string]bool{}
	for _, f := range findings {
		switch {
		case strings.Contains(*f.FieldPath, "status"):
			byProperty["status"] = true
		case strings.Contains(*f.FieldPath, "currency"):
			byProperty["currency"] = true
		default:
			t.Errorf("field_path %q names neither changed property", *f.FieldPath)
		}
	}
	if len(byProperty) != 2 {
		t.Errorf("the two findings do not separate the two properties: %v", byProperty)
	}
}

// TestRollingBackIsNotTheForwardChangeRecurring.
//
// Re-uploading the version you just replaced fires the same rule on the same
// endpoint as the change that replaced it. With no field_path the two shared a
// signature, so the rollback landed on the forward finding's row and bumped its
// occurrence_count — recording "this breaking change happened again" for an
// operator who had just undone it. The values are in the change's arguments, so
// the two directions now hold distinct signatures.
func TestRollingBackIsNotTheForwardChangeRecurring(t *testing.T) {
	forward, err := DetectVersionDiffData([]byte(vdTypeV1), []byte(vdTypeV2), "acme-payments")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	back, err := DetectVersionDiffData([]byte(vdTypeV2), []byte(vdTypeV1), "acme-payments")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(forward) != 1 || len(back) != 1 {
		t.Fatalf("want one finding each way, got forward=%d back=%d", len(forward), len(back))
	}
	if forward[0].Rule != back[0].Rule {
		t.Fatalf("the two directions fired different rules (%q, %q) — this test no longer covers the recurrence",
			forward[0].Rule, back[0].Rule)
	}
	if forward[0].Signature == back[0].Signature {
		t.Errorf("the rollback shares signature %q with the change it undoes — the store records it as that change recurring",
			forward[0].Signature)
	}
}

const vdTypeV1 = `
openapi: 3.0.0
info: { title: Acme, version: 1.0.0 }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  amount: { type: integer }
`

const vdTypeV2 = `
openapi: 3.0.0
info: { title: Acme, version: 2.0.0 }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  amount: { type: string }
`
