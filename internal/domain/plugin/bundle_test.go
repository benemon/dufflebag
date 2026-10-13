package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWriteBundleWritesManifestFirstAndFilesInManifestOrder(t *testing.T) {
	exportedAt := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	manifest := BundleManifest{
		Format: 1, ExportedAt: exportedAt,
		Instance:         BundleInstance{Host: "dufflebag.example.com", Organization: "acme"},
		DufflebagVersion: "v0.5.0-rc5",
		Plugins: []BundlePlugin{{
			Name: "amazon", Origin: BundleOrigin{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"},
			Versions: []BundleVersion{{Version: "1.8.2", Platforms: []string{"linux_amd64"}, Files: []BundleFile{
				{Name: "packer-plugin-amazon_v1.8.2_SHA256SUMS", SHA256: "sum", Size: 4, ObjectKey: "sums"},
				{Name: "packer-plugin-amazon_v1.8.2_x5.0_linux_amd64.zip", SHA256: "zip", Size: 3, ObjectKey: "zip"},
			}}},
		}},
	}
	content := map[string]string{"sums": "data", "zip": "zip"}
	var archive bytes.Buffer
	if err := WriteBundle(&archive, manifest, func(file BundleFile) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content[file.ObjectKey])), nil
	}); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	wantNames := []string{
		"manifest.json",
		"plugins/amazon/1.8.2/packer-plugin-amazon_v1.8.2_SHA256SUMS",
		"plugins/amazon/1.8.2/packer-plugin-amazon_v1.8.2_x5.0_linux_amd64.zip",
	}
	for i, want := range wantNames {
		header, err := tr.Next()
		if err != nil || header.Name != want {
			t.Fatalf("entry %d = %v %v, want %q", i, header, err, want)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			var got BundleManifest
			if err := json.Unmarshal(body, &got); err != nil || got.Format != 1 || got.Plugins[0].Versions[0].Files[0].ObjectKey != "" {
				t.Fatalf("manifest = %+v, %v", got, err)
			}
		}
	}
	if header, err := tr.Next(); err != io.EOF || header != nil {
		t.Fatalf("extra entry = %v %v", header, err)
	}
}
