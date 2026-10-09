package plugin

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/benemon/dufflebag/internal/domain/registry"
)

// testdata holds verbatim producer artifacts, captured 2026-10-09:
//   - packer-plugin-amazon 1.8.2 SHA256SUMS and manifest from releases.hashicorp.com
//     (release-site zip names, manifest listed in the sums)
//   - packer-plugin-git v0.6.3 SHA256SUMS from github.com/ethanmdavidson/packer-plugin-git
//     (goreleaser zip names)
//
// Zip entry lists are the real contents of packer-plugin-amazon_1.8.2_linux_arm64.zip
// and packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip from the same releases.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digestOf(t *testing.T, sums []byte, filename string) string {
	t.Helper()
	for _, line := range strings.Split(string(sums), "\n") {
		if strings.HasSuffix(line, "  "+filename) {
			return line[:64]
		}
	}
	t.Fatalf("%s not in sums", filename)
	return ""
}

var amazonLinuxArm64Entries = []string{"LICENSE.txt", "packer-plugin-amazon_v1.8.2_x5.0_linux_arm64"}

func amazonUpload(t *testing.T) Upload {
	sums := fixture(t, "packer-plugin-amazon_1.8.2_SHA256SUMS")
	zip := "packer-plugin-amazon_1.8.2_linux_arm64.zip"
	return Upload{
		Name: "amazon", Version: "1.8.2", Sums: sums,
		Manifest: fixture(t, "packer-plugin-amazon_1.8.2_manifest.json"),
		Zips:     []UploadedZip{{Filename: zip, SHA256: digestOf(t, sums, zip), Entries: amazonLinuxArm64Entries}},
	}
}

func gitUpload(t *testing.T) Upload {
	sums := fixture(t, "packer-plugin-git_v0.6.3_SHA256SUMS")
	zip := "packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip"
	return Upload{
		Name: "git", Version: "0.6.3", Sums: sums,
		Zips: []UploadedZip{{Filename: zip, SHA256: digestOf(t, sums, zip), Entries: []string{"packer-plugin-git_v0.6.3_x5.0_linux_arm64"}}},
	}
}

func TestVerifyAcceptsBothPublishingShapes(t *testing.T) {
	amazon, err := Verify(amazonUpload(t))
	if err != nil {
		t.Fatalf("releases.hashicorp.com shape: %v", err)
	}
	if len(amazon.Zips) != 1 || amazon.Zips[0].Protocol != "5.0" || amazon.Zips[0].Platform != (Platform{"linux", "arm64"}) {
		t.Fatalf("amazon zips = %+v", amazon.Zips)
	}
	if amazon.Protocol != "" {
		t.Fatalf("amazon supplied its manifest, so no rendered protocol; got %q", amazon.Protocol)
	}
	if len(amazon.Listed) != 18 {
		t.Fatalf("amazon lists %d platforms, want 18", len(amazon.Listed))
	}

	git, err := Verify(gitUpload(t))
	if err != nil {
		t.Fatalf("goreleaser shape: %v", err)
	}
	if git.Zips[0].Protocol != "5.0" || git.Protocol != "" {
		t.Fatalf("git = %+v", git)
	}
}

func TestVerifyResolvesProtocolFromTheInnerBinaryWithoutAManifest(t *testing.T) {
	upload := amazonUpload(t)
	upload.Manifest = nil
	var kept []string
	for _, line := range strings.Split(string(upload.Sums), "\n") {
		if !strings.Contains(line, "manifest.json") {
			kept = append(kept, line)
		}
	}
	upload.Sums = []byte(strings.Join(kept, "\n"))
	verified, err := Verify(upload)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Protocol != "5.0" {
		t.Fatalf("rendered protocol = %q, want 5.0 from the goreleaser-named binary", verified.Protocol)
	}
}

func TestVerifyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Upload)
		want   string
	}{
		{"digest mismatch", func(u *Upload) { u.Zips[0].SHA256 = strings.Repeat("0", 64) }, "digest does not match"},
		{"zip not listed", func(u *Upload) { u.Zips[0].Filename = "packer-plugin-amazon_1.8.2_plan9_amd64.zip" }, "not listed"},
		{"zip uploaded twice", func(u *Upload) { u.Zips = append(u.Zips, u.Zips[0]) }, "uploaded twice"},
		{"listed manifest missing", func(u *Upload) { u.Manifest = nil }, "must be uploaded"},
		{"manifest altered", func(u *Upload) { u.Manifest = append([]byte(nil), append(u.Manifest, ' ')...) }, "digest does not match"},
		{"version mismatch", func(u *Upload) { u.Version = "1.8.3" }, "not a zip name Packer accepts"},
		{"v-prefixed version", func(u *Upload) { u.Version = "v1.8.2" }, "semantic version"},
		{"uppercase name", func(u *Upload) { u.Name = "Amazon" }, "lowercase"},
		{"packer- prefix", func(u *Upload) { u.Name = "packer-amazon" }, "packer- prefix"},
		{"double hyphen", func(u *Upload) { u.Name = "a--b" }, "single hyphens"},
		{"no zips", func(u *Upload) { u.Zips = nil }, "at least one zip"},
		{"malformed sums line", func(u *Upload) { u.Sums = append([]byte("not a digest\n"), u.Sums...) }, "line 1"},
		{"foreign zip in sums", func(u *Upload) {
			u.Sums = append(u.Sums, []byte(strings.Repeat("a", 64)+"  packer-plugin-other_1.8.2_linux_amd64.zip\n")...)
		}, "not a zip name Packer accepts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upload := amazonUpload(t)
			tc.mutate(&upload)
			_, err := Verify(upload)
			if !errors.Is(err, registry.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestVerifyRefusesAnUnresolvableProtocol(t *testing.T) {
	upload := amazonUpload(t)
	upload.Manifest = nil
	var kept []string
	for _, line := range strings.Split(string(upload.Sums), "\n") {
		if !strings.Contains(line, "manifest.json") {
			kept = append(kept, line)
		}
	}
	upload.Sums = []byte(strings.Join(kept, "\n"))
	upload.Zips[0].Entries = []string{"LICENSE.txt", "packer-plugin-amazon"}
	if _, err := Verify(upload); err == nil || !strings.Contains(err.Error(), "no plugin protocol") {
		t.Fatalf("err = %v, want refusal for an unresolvable protocol", err)
	}
}

func TestValidVersion(t *testing.T) {
	for version, want := range map[string]bool{
		"1.8.2": true, "0.6.3": true, "1.0.0-beta.1": true,
		"v1.8.2": false, "1.8": false, "01.2.3": false, "1.2.3+build": false, "": false,
	} {
		if got := validVersion(version); got != want {
			t.Errorf("validVersion(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestNewestFollowsSemanticVersionPrecedence(t *testing.T) {
	for want, versions := range map[string][]string{
		"1.10.0":  {"1.9.0", "1.10.0", "1.2.0"},
		"1.0.0":   {"1.0.0-rc.1", "1.0.0", "1.0.0-beta.11"},
		"2.0.0-2": {"2.0.0-1", "2.0.0-2", "1.9.9"},
		"1.0.0-b": {"1.0.0-a", "1.0.0-b", "1.0.0-1"},
		"":        nil,
	} {
		if got := Newest(versions); got != want {
			t.Errorf("Newest(%v) = %q, want %q", versions, got, want)
		}
	}
}
