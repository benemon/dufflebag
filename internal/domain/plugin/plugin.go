// Package plugin holds the rules a Packer plugin version must satisfy before
// the registry stores it (ADR-0027). The shapes are fixed by Packer's remote
// getter (hashicorp/packer plugin-getter/remote/getter.go) and its source
// address parser (hcl2template/addrs/plugin.go), not chosen here.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/benemon/dufflebag/internal/domain/registry"
)

// Packer validates the final source-address component as a DNS label and
// rejects the "packer-" prefix (ParsePluginPart, ParsePluginSourceString).
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validName reports whether name can be the last component of a source address.
func validName(name string) bool {
	return len(name) <= 63 && namePattern.MatchString(name) && !strings.HasPrefix(name, "packer-")
}

// Build metadata is excluded: the getter builds version directories from the
// version string, and "+" has no stable meaning in a URL path.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// validVersion reports whether version is a canonical, unprefixed semantic version.
func validVersion(version string) bool {
	return versionPattern.MatchString(version)
}

var protocolPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// Platform is an operating system and architecture pair.
type Platform struct {
	OS   string
	Arch string
}

// Zip is one zip entry of a version's SHA256SUMS. Protocol is empty for the
// release-site shape, which carries none in its filename.
type Zip struct {
	Filename string
	SHA256   string
	Platform Platform
	Protocol string
}

// Sums is a parsed SHA256SUMS file for one plugin version.
type Sums struct {
	Zips           []Zip
	ManifestSHA256 string
}

// UploadedZip is a zip the publisher supplied: its name, the digest of its
// bytes, and the names of the files inside it.
type UploadedZip struct {
	Filename string
	SHA256   string
	Entries  []string
}

// Upload is a complete file set for one version.
type Upload struct {
	Name     string
	Version  string
	Sums     []byte
	Manifest []byte
	Zips     []UploadedZip
}

// Verified is an accepted upload. Protocol is set only when Packer will ask
// for a manifest the publisher did not supply, so the registry renders one.
type Verified struct {
	Zips     []Zip
	Listed   []Platform
	Protocol string
}

func refuse(file, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", registry.ErrInvalid, file, fmt.Sprintf(format, args...))
}

// ManifestName is the manifest filename Packer requests for a version.
func ManifestName(name, version string) string {
	return fmt.Sprintf("packer-plugin-%s_%s_manifest.json", name, version)
}

// SumsName is the SHA256SUMS filename Packer requests for a version. The
// stored file keeps whatever name the publisher gave it; this is its address.
func SumsName(name, version string) string {
	return fmt.Sprintf("packer-plugin-%s_%s_SHA256SUMS", name, version)
}

// SignatureName is the detached signature's address beside SumsName.
func SignatureName(name, version string) string {
	return SumsName(name, version) + ".sig"
}

// RenderManifest is the manifest served when the publisher supplied none, in
// the shape releases.hashicorp.com publishes. Packer reads only
// metadata.protocol_version (remote/getter.go resolveProtocolVersion).
func RenderManifest(protocol string) []byte {
	return []byte(fmt.Sprintf("{\n  \"version\": \"1\",\n  \"metadata\": {\n    \"protocol_version\": %q\n  }\n}", protocol))
}

// ListsManifest reports whether a SHA256SUMS file names the version's
// manifest, in either of sha256sum's separators.
func ListsManifest(name, version string, sums []byte) (bool, error) {
	parsed, err := parseSums(name, version, sums)
	if err != nil {
		return false, err
	}
	return parsed.ManifestSHA256 != "", nil
}

// parseSums parses a SHA256SUMS file in sha256sum's output format.
func parseSums(name, version string, data []byte) (Sums, error) {
	const file = "SHA256SUMS"
	goreleaser := regexp.MustCompile(`^packer-plugin-` + regexp.QuoteMeta(name) + `_v` + regexp.QuoteMeta(version) +
		`_x([0-9]+\.[0-9]+)_([a-z0-9]+)_([a-z0-9]+)\.zip$`)
	releaseSite := regexp.MustCompile(`^packer-plugin-` + regexp.QuoteMeta(name) + `_` + regexp.QuoteMeta(version) +
		`_([a-z0-9]+)_([a-z0-9]+)\.zip$`)
	line := regexp.MustCompile(`^([0-9a-f]{64}) [ *]([^/\s]+)$`)

	var sums Sums
	seenFiles := map[string]bool{}
	seenPlatforms := map[Platform]bool{}
	for number, text := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		parts := line.FindStringSubmatch(text)
		if parts == nil {
			return Sums{}, refuse(file, "line %d is not a SHA-256 digest and a filename", number+1)
		}
		digest, filename := parts[1], parts[2]
		if seenFiles[filename] {
			return Sums{}, refuse(file, "%s is listed twice", filename)
		}
		seenFiles[filename] = true
		if filename == ManifestName(name, version) {
			sums.ManifestSHA256 = digest
			continue
		}
		if !strings.HasSuffix(filename, ".zip") {
			continue
		}
		zip := Zip{Filename: filename, SHA256: digest}
		if m := goreleaser.FindStringSubmatch(filename); m != nil {
			zip.Protocol, zip.Platform = m[1], Platform{OS: m[2], Arch: m[3]}
		} else if m := releaseSite.FindStringSubmatch(filename); m != nil {
			zip.Platform = Platform{OS: m[1], Arch: m[2]}
		} else {
			return Sums{}, refuse(file, "%s is not a zip name Packer accepts for %s %s", filename, name, version)
		}
		if seenPlatforms[zip.Platform] {
			return Sums{}, refuse(file, "%s_%s is listed twice", zip.Platform.OS, zip.Platform.Arch)
		}
		seenPlatforms[zip.Platform] = true
		sums.Zips = append(sums.Zips, zip)
	}
	if len(sums.Zips) == 0 {
		return Sums{}, refuse(file, "lists no zips")
	}
	return sums, nil
}

// Verify checks a complete upload. Every uploaded zip must be listed with a
// matching digest; listed zips may be absent (ADR-0027 D9).
func Verify(upload Upload) (Verified, error) {
	if !validName(upload.Name) {
		return Verified{}, refuse("name", "%q must be lowercase letters, digits and single hyphens, without the packer- prefix", upload.Name)
	}
	if !validVersion(upload.Version) {
		return Verified{}, refuse("version", "%q must be a semantic version without a v prefix or build metadata", upload.Version)
	}
	sums, err := parseSums(upload.Name, upload.Version, upload.Sums)
	if err != nil {
		return Verified{}, err
	}
	if len(upload.Zips) == 0 {
		return Verified{}, refuse("zips", "at least one zip is required")
	}

	listed := map[string]Zip{}
	verified := Verified{}
	for _, zip := range sums.Zips {
		listed[zip.Filename] = zip
		verified.Listed = append(verified.Listed, zip.Platform)
	}

	manifestProtocol := ""
	if upload.Manifest != nil {
		if sums.ManifestSHA256 != "" && sha256Hex(upload.Manifest) != sums.ManifestSHA256 {
			return Verified{}, refuse(ManifestName(upload.Name, upload.Version), "digest does not match SHA256SUMS")
		}
		var manifest struct {
			Metadata struct {
				ProtocolVersion string `json:"protocol_version"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(bytes.NewReader(upload.Manifest)).Decode(&manifest); err != nil ||
			!protocolPattern.MatchString(manifest.Metadata.ProtocolVersion) {
			return Verified{}, refuse(ManifestName(upload.Name, upload.Version), "has no metadata.protocol_version")
		}
		manifestProtocol = manifest.Metadata.ProtocolVersion
	} else if sums.ManifestSHA256 != "" {
		return Verified{}, refuse(ManifestName(upload.Name, upload.Version), "SHA256SUMS lists it, so it must be uploaded")
	}

	seen := map[string]bool{}
	innerProtocol := ""
	for _, uploaded := range upload.Zips {
		zip, ok := listed[uploaded.Filename]
		if !ok {
			return Verified{}, refuse(uploaded.Filename, "is not listed in SHA256SUMS")
		}
		if seen[uploaded.Filename] {
			return Verified{}, refuse(uploaded.Filename, "is uploaded twice")
		}
		seen[uploaded.Filename] = true
		if uploaded.SHA256 != zip.SHA256 {
			return Verified{}, refuse(uploaded.Filename, "digest does not match SHA256SUMS")
		}
		if zip.Protocol == "" && manifestProtocol == "" {
			protocol, err := binaryProtocol(upload.Name, upload.Version, zip, uploaded.Entries)
			if err != nil {
				return Verified{}, err
			}
			if innerProtocol != "" && protocol != innerProtocol {
				return Verified{}, refuse(uploaded.Filename, "plugin protocol %s differs from %s in another zip", protocol, innerProtocol)
			}
			innerProtocol = protocol
			zip.Protocol = protocol
		} else if zip.Protocol == "" {
			zip.Protocol = manifestProtocol
		}
		verified.Zips = append(verified.Zips, zip)
	}

	for _, zip := range sums.Zips {
		if zip.Protocol == "" && manifestProtocol == "" {
			if innerProtocol == "" {
				return Verified{}, refuse("SHA256SUMS", "%s carries no plugin protocol and no uploaded zip or manifest supplies one", zip.Filename)
			}
			verified.Protocol = innerProtocol
			break
		}
	}
	return verified, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The binary inside a release-site zip is goreleaser-named; its name is the
// only evidence of the protocol when no manifest is supplied.
func binaryProtocol(name, version string, zip Zip, entries []string) (string, error) {
	binary := regexp.MustCompile(`^packer-plugin-` + regexp.QuoteMeta(name) + `_v` + regexp.QuoteMeta(version) +
		`_x([0-9]+\.[0-9]+)_` + regexp.QuoteMeta(zip.Platform.OS) + `_` + regexp.QuoteMeta(zip.Platform.Arch) + `(\.exe)?$`)
	for _, entry := range entries {
		if m := binary.FindStringSubmatch(entry); m != nil {
			return m[1], nil
		}
	}
	return "", refuse(zip.Filename, "no plugin protocol in its filename, no manifest, and no goreleaser-named binary inside")
}

// Newest returns the highest version by semantic version precedence, or ""
// when versions is empty. Versions must already be valid.
func Newest(versions []string) string {
	newest := ""
	for _, version := range versions {
		if newest == "" || Compare(version, newest) > 0 {
			newest = version
		}
	}
	return newest
}

// Compare orders two valid versions by semantic version precedence.
func Compare(a, b string) int {
	coreA, preA, _ := strings.Cut(a, "-")
	coreB, preB, _ := strings.Cut(b, "-")
	partsA, partsB := strings.Split(coreA, "."), strings.Split(coreB, ".")
	for i := range partsA {
		if c := compareNumeric(partsA[i], partsB[i]); c != 0 {
			return c
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	identsA, identsB := strings.Split(preA, "."), strings.Split(preB, ".")
	for i := 0; i < len(identsA) && i < len(identsB); i++ {
		numericA, numericB := isNumeric(identsA[i]), isNumeric(identsB[i])
		var c int
		switch {
		case numericA && numericB:
			c = compareNumeric(identsA[i], identsB[i])
		case numericA:
			c = -1
		case numericB:
			c = 1
		default:
			c = strings.Compare(identsA[i], identsB[i])
		}
		if c != 0 {
			return c
		}
	}
	return len(identsA) - len(identsB)
}

func isNumeric(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}

// Numeric identifiers carry no leading zeros (validated), so length orders them.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
