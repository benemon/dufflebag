package contract_test

import (
	"os"
	"regexp"
	"runtime/debug"
	"testing"
)

// The client stack is pinned to what Packer v1.16.0 links against (dossier
// section 9a): hcp-sdk-go v0.174.0, go-openapi/runtime v0.32.3, and
// go-openapi/strfmt v0.26.3. strfmt is wire-visible — it decides how
// strfmt.DateTime serialises — and the SERVING module renders it, so the pin
// only binds when the root module carries the same version this module decodes
// with. A "keep these in step" comment did not hold (duf-nsu); this does.
// Bumping any value here is a deliberate, reviewable event tied to declaring
// support for a new Packer release, never an automated dependency update.
const (
	pinnedStrfmt  = "v0.26.3"
	pinnedSDK     = "v0.174.0"
	pinnedRuntime = "v0.32.3"
)

func requireVersion(t *testing.T, gomod, module, want string) {
	t.Helper()
	data, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatalf("read %s: %v", gomod, err)
	}
	// Anchored to a require entry: tab-indented module, version, and nothing
	// after but an optional indirect comment. A comment naming the module or a
	// tab-indented line in a replace block ("module vX => ./local") cannot
	// satisfy the pin.
	m := regexp.MustCompile(`(?m)^\t` + regexp.QuoteMeta(module) + ` (v[0-9][^\s]*)( // indirect)?$`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s does not require %s", gomod, module)
	}
	if got := string(m[1]); got != want {
		t.Fatalf("%s pins %s %s, want %s", gomod, module, got, want)
	}
}

// This module builds in the go.work workspace, where versions resolve across
// both modules and contract/go.mod is never re-tidied, so its text can lag
// what the test binary links. The binary's own build info is the authority.
func requireLinked(t *testing.T, module, want string) {
	t.Helper()
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("test binary carries no build info")
	}
	for _, d := range bi.Deps {
		if d.Path == module {
			if d.Replace != nil {
				t.Fatalf("%s is replaced by %s", module, d.Replace.Path)
			}
			if d.Version != want {
				t.Fatalf("contract links %s %s, want %s", module, d.Version, want)
			}
			return
		}
	}
	t.Fatalf("contract does not link %s", module)
}

func TestPinnedClientStackBindsBothModules(t *testing.T) {
	requireLinked(t, "github.com/go-openapi/strfmt", pinnedStrfmt)
	requireVersion(t, "../go.mod", "github.com/go-openapi/strfmt", pinnedStrfmt)
	requireLinked(t, "github.com/hashicorp/hcp-sdk-go", pinnedSDK)
	requireLinked(t, "github.com/go-openapi/runtime", pinnedRuntime)
}
