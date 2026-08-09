package postgres

import (
	"bytes"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func compressedSBOM(t *testing.T, document string) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := encoder.Close(); err != nil {
			t.Errorf("close zstd encoder: %v", err)
		}
	}()
	return encoder.EncodeAll([]byte(document), nil)
}

func TestParseSPDXExcludesDescribedRootAndCapturesReportedFields(t *testing.T) {
	packages, err := parseSbom(compressedSBOM(t, `{
		"spdxVersion":"SPDX-2.3", "SPDXID":"SPDXRef-DOCUMENT",
		"packages":[
			{"name":"image-root","SPDXID":"SPDXRef-Root","versionInfo":"1"},
			{"name":"openssl","SPDXID":"SPDXRef-RPM","versionInfo":"3.0.11",
			 "licenseConcluded":"NOASSERTION","licenseDeclared":"Apache-2.0",
			 "licenseInfoFromFiles":["Apache-2.0","MIT"],
			 "externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:rpm/openssl@3.0.11"}]},
			{"name":"openssl","SPDXID":"SPDXRef-NPM","versionInfo":"3.0.11",
			 "externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/openssl@3.0.11"}]}
		],
		"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relatedSpdxElement":"SPDXRef-Root","relationshipType":"DESCRIBES"}]
	}`), "SPDX")
	if err != nil {
		t.Fatalf("parse SPDX: %v", err)
	}
	if len(packages) != 2 {
		t.Fatalf("packages = %#v, want two content packages and no described root", packages)
	}
	if packages[0].Purl != "pkg:npm/openssl@3.0.11" ||
		packages[1].Purl != "pkg:rpm/openssl@3.0.11" {
		t.Fatalf("purl identities = %#v", packages)
	}
	if got := packages[1].Licenses; len(got) != 2 || got[0] != "Apache-2.0" || got[1] != "MIT" {
		t.Fatalf("SPDX licenses = %#v, want declared/file licenses without NOASSERTION", got)
	}
}

func TestParseCycloneDXFlattensNestedComponentsAndPreservesPaths(t *testing.T) {
	packages, err := parseSbom(compressedSBOM(t, `{
		"bomFormat":"CycloneDX","specVersion":"1.6","components":[
			{"bom-ref":"parent-a","name":"parent","version":"1","licenses":[{"license":{"id":"MIT"}}],"components":[
				{"bom-ref":"child-a","name":"child","version":"2","purl":"pkg:npm/child@2","licenses":[{"expression":"MIT OR Apache-2.0"}]}
			]},
			{"bom-ref":"parent-b","name":"other-parent","components":[
				{"bom-ref":"child-b","name":"child","version":"2","purl":"pkg:npm/child@2"}
			]}
		]
	}`), "CYCLONEDX")
	if err != nil {
		t.Fatalf("parse CycloneDX: %v", err)
	}
	if len(packages) != 3 {
		t.Fatalf("flattened packages = %#v, want parent, other-parent, and one merged child", packages)
	}
	var child ReportedPackage
	for _, pkg := range packages {
		if pkg.Name == "child" {
			child = pkg
		}
	}
	if child.Purl != "pkg:npm/child@2" || len(child.ComponentPaths) != 2 {
		t.Fatalf("nested child = %#v", child)
	}
	if got := child.ComponentPaths[0]; len(got) != 2 || got[0] != "parent-a" || got[1] != "child-a" {
		t.Fatalf("first containment path = %#v", got)
	}
	if len(child.Licenses) != 1 || child.Licenses[0] != "MIT OR Apache-2.0" {
		t.Fatalf("CycloneDX licenses = %#v", child.Licenses)
	}
}

func TestParseSbomDistinguishesEmptyFromUnparseable(t *testing.T) {
	empty, err := parseSbom(compressedSBOM(t,
		`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[]}`), "SPDX")
	if err != nil || len(empty) != 0 {
		t.Fatalf("valid empty SPDX = %#v, %v", empty, err)
	}

	tests := []struct {
		name, format, document, want string
	}{
		{"invalid JSON", "SPDX", `{`, "invalid SPDX JSON"},
		{"SPDX marker", "SPDX", `{}`, "SPDX document marker missing"},
		{"invalid CycloneDX JSON", "CYCLONEDX", `{`, "invalid CycloneDX JSON"},
		{"CycloneDX marker", "CYCLONEDX", `{}`, "CycloneDX document marker missing"},
		{"unsupported format", "UNKNOWN", `{}`, `unsupported SBOM format "UNKNOWN"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseSbom(compressedSBOM(t, test.document), test.format); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := parseSbom([]byte("not-zstd"), "SPDX"); err == nil ||
		!strings.Contains(err.Error(), "zstd decompression failed") {
		t.Fatalf("corrupt zstd error = %v", err)
	}
}

func TestParseSbomRefusesOversizedExpansion(t *testing.T) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := encoder.Close(); err != nil {
			t.Errorf("close zstd encoder: %v", err)
		}
	}()
	compressed := encoder.EncodeAll(bytes.Repeat([]byte("x"), maxDecompressedSbomBytes+1), nil)
	if _, err := parseSbom(compressed, "SPDX"); err == nil ||
		!strings.Contains(err.Error(), "decompressed SBOM exceeds") {
		t.Fatalf("oversized expansion error = %v", err)
	}
}
