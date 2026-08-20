package redact

// ZERO-EXTERNAL-CALLS sentinel — the Go twin of the TS no-network.spec.ts. The floor
// must never do I/O: it runs on every captured body, before anything is stored or
// transmitted, inside the customer's cluster.
//
// govalidator links `net` for its DNS/host helpers (IsDialString, IsHost,
// IsExistingEmail); the functions we call (IsEmail, IsSSN, IsIPv4, IsIPv6) are pure
// parsers/regexes. This test is the lint ban that keeps it that way: it fails the
// moment any non-test file in this package names one of the network-capable helpers
// or imports a package that can open a connection or a process. The CI net-audit
// step additionally bans net/http, os/exec and crypto/tls from the package's whole
// dependency graph.

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestFloorNeverDoesIO(t *testing.T) {
	bannedCalls := []string{"IsExistingEmail", "IsDialString", "IsHost("}
	bannedImports := map[string]string{
		"net/http":   "HTTP client/server",
		"os/exec":    "process execution",
		"net":        "sockets and DNS",
		"crypto/tls": "TLS dialing",
		"os":         "the floor must not touch the OS at all (files, env, process)",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, call := range bannedCalls {
			if strings.Contains(string(src), call) {
				t.Errorf("%s references banned network-capable helper %q", name, call)
			}
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, banned := bannedImports[path]; banned {
				t.Errorf("%s imports banned package %q (%s)", name, path, why)
			}
		}
	}
}
