package postgres

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const maxDecompressedSbomBytes = 64 << 20

type spdxDocument struct {
	SPDXVersion   string             `json:"spdxVersion"`
	SPDXID        string             `json:"SPDXID"`
	Packages      []spdxPackage      `json:"packages"`
	Relationships []spdxRelationship `json:"relationships"`
}

type spdxPackage struct {
	Name                 string            `json:"name"`
	SPDXID               string            `json:"SPDXID"`
	Version              string            `json:"versionInfo"`
	LicenseConcluded     string            `json:"licenseConcluded"`
	LicenseDeclared      string            `json:"licenseDeclared"`
	LicenseInfoFromFiles []string          `json:"licenseInfoFromFiles"`
	ExternalRefs         []spdxExternalRef `json:"externalRefs"`
}

type spdxRelationship struct {
	ElementID      string `json:"spdxElementId"`
	RelatedElement string `json:"relatedSpdxElement"`
	Type           string `json:"relationshipType"`
}

type spdxExternalRef struct {
	Type    string `json:"referenceType"`
	Locator string `json:"referenceLocator"`
}

type cycloneDXDocument struct {
	BOMFormat  string               `json:"bomFormat"`
	Components []cycloneDXComponent `json:"components"`
}

type cycloneDXComponent struct {
	BOMRef     string               `json:"bom-ref"`
	Name       string               `json:"name"`
	Version    string               `json:"version"`
	Purl       string               `json:"purl"`
	Licenses   []cycloneDXLicense   `json:"licenses"`
	Components []cycloneDXComponent `json:"components"`
}

type cycloneDXLicense struct {
	Expression string `json:"expression"`
	License    struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"license"`
}

// decompressSbom opens the zstd envelope the hcp-sbom provisioner wraps every
// document in, bounded exactly as parsing is.
func decompressSbom(compressed []byte) ([]byte, error) {
	reader, err := zstd.NewReader(bytes.NewReader(compressed),
		zstd.WithDecoderMaxMemory(maxDecompressedSbomBytes))
	if err != nil {
		return nil, fmt.Errorf("zstd decompression failed: %w", err)
	}
	decompressed, err := io.ReadAll(io.LimitReader(reader, maxDecompressedSbomBytes+1))
	reader.Close()
	if err != nil {
		return nil, fmt.Errorf("zstd decompression failed: %w", err)
	}
	if len(decompressed) > maxDecompressedSbomBytes {
		return nil, fmt.Errorf("decompressed SBOM exceeds %d bytes", maxDecompressedSbomBytes)
	}
	return decompressed, nil
}

func parseSbom(compressed []byte, format string) ([]ReportedPackage, error) {
	decompressed, err := decompressSbom(compressed)
	if err != nil {
		return nil, err
	}

	switch format {
	case "SPDX":
		return parseSPDX(decompressed)
	case "CYCLONEDX":
		return parseCycloneDX(decompressed)
	default:
		return nil, fmt.Errorf("unsupported SBOM format %q", format)
	}
}

func parseSPDX(document []byte) ([]ReportedPackage, error) {
	var parsed spdxDocument
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, fmt.Errorf("invalid SPDX JSON: %w", err)
	}
	if !strings.HasPrefix(parsed.SPDXVersion, "SPDX-") || parsed.SPDXID == "" {
		return nil, fmt.Errorf("SPDX document marker missing")
	}

	// SPDX generators commonly put the image/directory itself in packages and
	// have the document DESCRIBE it. HCP omits that self-entry. A real
	// Packer/Syft document generated from this repository had 244 packages and
	// described SPDXRef-DocumentRoot-Directory-.; excluding DESCRIBES targets
	// retained the 243 reported contents rather than listing the image as one of
	// its own packages.
	described := make(map[string]bool)
	for _, relationship := range parsed.Relationships {
		if relationship.Type == "DESCRIBES" && relationship.ElementID == parsed.SPDXID {
			described[relationship.RelatedElement] = true
		}
	}

	packages := make([]ReportedPackage, 0, len(parsed.Packages))
	for _, pkg := range parsed.Packages {
		if described[pkg.SPDXID] {
			continue
		}
		purl := ""
		for _, ref := range pkg.ExternalRefs {
			if strings.EqualFold(ref.Type, "purl") {
				purl = ref.Locator
				break
			}
		}
		licenses := append([]string{pkg.LicenseConcluded, pkg.LicenseDeclared},
			pkg.LicenseInfoFromFiles...)
		packages = append(packages, ReportedPackage{
			Name: pkg.Name, Version: pkg.Version, Purl: purl,
			Licenses: normalizedLicenses(licenses),
		})
	}
	return mergeReportedPackages(packages), nil
}

func parseCycloneDX(document []byte) ([]ReportedPackage, error) {
	var parsed cycloneDXDocument
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, fmt.Errorf("invalid CycloneDX JSON: %w", err)
	}
	if parsed.BOMFormat != "CycloneDX" {
		return nil, fmt.Errorf("CycloneDX document marker missing")
	}

	packages := make([]ReportedPackage, 0, len(parsed.Components))
	var flatten func([]cycloneDXComponent, []string)
	flatten = func(components []cycloneDXComponent, parentPath []string) {
		for _, component := range components {
			segment := component.BOMRef
			if segment == "" {
				segment = component.Name
				if component.Version != "" {
					segment += "@" + component.Version
				}
			}
			path := append(append([]string(nil), parentPath...), segment)
			licenses := make([]string, 0, len(component.Licenses))
			for _, license := range component.Licenses {
				licenses = append(licenses, license.Expression, license.License.ID, license.License.Name)
			}
			// HCP's package response is flat. Preserve each bom-ref/name ancestry
			// path in storage while emitting one row per package identity, so the
			// containment expressed by recursive components is not thrown away.
			packages = append(packages, ReportedPackage{
				Name: component.Name, Version: component.Version, Purl: component.Purl,
				Licenses: normalizedLicenses(licenses), ComponentPaths: [][]string{path},
			})
			flatten(component.Components, path)
		}
	}
	flatten(parsed.Components, nil)
	return mergeReportedPackages(packages), nil
}

func normalizedLicenses(values []string) []string {
	seen := make(map[string]bool)
	licenses := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || value == "NOASSERTION" || value == "NONE" || seen[value] {
			continue
		}
		seen[value] = true
		licenses = append(licenses, value)
	}
	sort.Strings(licenses)
	return licenses
}

func mergeReportedPackages(packages []ReportedPackage) []ReportedPackage {
	type key struct{ name, version, purl string }
	merged := make(map[key]ReportedPackage, len(packages))
	for _, pkg := range packages {
		identity := key{pkg.Name, pkg.Version, pkg.Purl}
		current := merged[identity]
		current.Name, current.Version, current.Purl = pkg.Name, pkg.Version, pkg.Purl
		current.Licenses = normalizedLicenses(append(current.Licenses, pkg.Licenses...))
		current.ComponentPaths = append(current.ComponentPaths, pkg.ComponentPaths...)
		merged[identity] = current
	}
	result := make([]ReportedPackage, 0, len(merged))
	for _, pkg := range merged {
		result = append(result, pkg)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		return left.Name < right.Name || left.Name == right.Name &&
			(left.Version < right.Version || left.Version == right.Version && left.Purl < right.Purl)
	})
	return result
}
