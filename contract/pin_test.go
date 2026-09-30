package contract_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
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
// what the build uses. The toolchain's resolved build list is the authority;
// test-binary build info is not, because Go 1.26 omits its dependencies.
func requireResolved(t *testing.T, module, want string) {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}{{if .Replace}} => {{.Replace.Path}}{{end}}", module).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m %s: %v\n%s", module, err, out)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("contract resolves %s %s, want %s", module, got, want)
	}
}

func TestPinnedClientStackBindsBothModules(t *testing.T) {
	requireResolved(t, "github.com/go-openapi/strfmt", pinnedStrfmt)
	requireVersion(t, "../go.mod", "github.com/go-openapi/strfmt", pinnedStrfmt)
	requireResolved(t, "github.com/hashicorp/hcp-sdk-go", pinnedSDK)
	requireResolved(t, "github.com/go-openapi/runtime", pinnedRuntime)
}
