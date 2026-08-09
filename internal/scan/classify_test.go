package scan

import (
	"encoding/json"
	"os"
	"testing"
)

// TestClassifySpikeManifest pins Classify to the fixtures recorded by the
// OSV.dev spike (duf-o0ou.1). Every fixture's class and translated query were
// established against live api.osv.dev behaviour on 2026-08-06; the manifest
// records the evidence for each expectation.
func TestClassifySpikeManifest(t *testing.T) {
	raw, err := os.ReadFile("testdata/spike-manifest.json")
	if err != nil {
		t.Fatalf("reading spike manifest: %v", err)
	}
	var manifest struct {
		Fixtures []struct {
			Purl  string `json:"purl"`
			Class string `json:"class"`
			Query *struct {
				Ecosystem   string `json:"ecosystem"`
				Name        string `json:"name"`
				Version     string `json:"version"`
				RedHatMajor string `json:"redhat_major"`
			} `json:"query"`
			Note string `json:"note"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parsing spike manifest: %v", err)
	}
	if len(manifest.Fixtures) == 0 {
		t.Fatal("spike manifest carries no fixtures")
	}

	for _, f := range manifest.Fixtures {
		class, query, reason := Classify(f.Purl)
		if string(class) != f.Class {
			t.Errorf("Classify(%q) class = %q (reason %q), manifest expects %q", f.Purl, class, reason, f.Class)
			continue
		}
		if class != ClassQueryable {
			if query != nil {
				t.Errorf("Classify(%q) returned a query for non-queryable class %q", f.Purl, class)
			}
			if reason == "" {
				t.Errorf("Classify(%q) returned class %q with no reason", f.Purl, class)
			}
			continue
		}
		if f.Query == nil {
			t.Errorf("manifest fixture %q is queryable but records no expected query", f.Purl)
			continue
		}
		if query == nil {
			t.Errorf("Classify(%q) returned queryable with a nil query", f.Purl)
			continue
		}
		if query.Ecosystem != f.Query.Ecosystem || query.Name != f.Query.Name || query.Version != f.Query.Version || query.RedHatMajor != f.Query.RedHatMajor {
			t.Errorf("Classify(%q) query = %+v, manifest expects %+v", f.Purl, *query, *f.Query)
		}
	}
}
