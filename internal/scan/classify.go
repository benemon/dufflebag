// Package scan orchestrates external vulnerability scanners over stored SBOM
// package inventories. Dufflebag never scans in-process and never owns a
// vulnerability feed; this package translates purls into provider queries and
// reports attributed findings.
package scan

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Class is the deterministic pre-query classification of an SBOM package purl.
// Classification runs before any network traffic: OSV answers {} for both
// "supported, zero findings" and "ecosystem not covered", so coverage can only
// be asserted here, never inferred from a response.
type Class string

const (
	ClassQueryable   Class = "queryable"
	ClassEmpty       Class = "empty"
	ClassInvalid     Class = "invalid"
	ClassUnversioned Class = "unversioned"
	ClassUnsupported Class = "unsupported"
)

// Query is the OSV ecosystem-form query a queryable purl translates to.
// Purl-form queries are never issued: Alpine purl matching returns nothing at
// all, deb/rpm purl matching evaluates every distribution stream, and rpm
// epochs ride in a qualifier the purl form ignores (spike duf-o0ou.1,
// testdata/spike-manifest.json).
type Query struct {
	Ecosystem string
	Name      string
	Version   string
	// RedHatMajor carries the enterprise_linux major from the purl's distro
	// qualifier (rhel-9.8 -> "9"). Set only for Ecosystem "Red Hat", where the
	// adapter's phase-2 stream confirmation needs it; empty otherwise.
	RedHatMajor string
}

var (
	alpineDistro = regexp.MustCompile(`^alpine-(\d+)\.(\d+)`)
	debianDistro = regexp.MustCompile(`^debian-(\d+)`)
	ubuntuDistro = regexp.MustCompile(`^ubuntu-(\d+\.\d+)`)
	redhatDistro = regexp.MustCompile(`^rhel-(\d+)`)
)

// Classify maps one purl to its class and, when queryable, the ecosystem-form
// query to issue. The reason string explains every non-queryable class.
func Classify(purl string) (Class, *Query, string) {
	if purl == "" {
		return ClassEmpty, nil, "component carries no purl"
	}
	p, err := parsePurl(purl)
	if err != nil {
		return ClassInvalid, nil, err.Error()
	}
	if p.version == "" {
		return ClassUnversioned, nil, "purl carries no version; a versionless OSV query returns the package's entire advisory history"
	}

	name := p.name
	if upstream := strings.SplitN(p.qualifiers["upstream"], " ", 2)[0]; upstream != "" && (p.ptype == "apk" || p.ptype == "deb") {
		// Alpine and Debian advisories key source/origin package names;
		// Syft records the mapping in the upstream qualifier.
		name = upstream
	}

	switch {
	case p.ptype == "apk" && p.namespace == "alpine":
		m := alpineDistro.FindStringSubmatch(p.qualifiers["distro"])
		if m == nil {
			return ClassUnsupported, nil, "apk purl without an alpine-X.Y distro qualifier: the Alpine:vX.Y stream cannot be selected and fixed versions differ per stream"
		}
		return ClassQueryable, &Query{Ecosystem: fmt.Sprintf("Alpine:v%s.%s", m[1], m[2]), Name: name, Version: p.version}, ""
	case p.ptype == "deb" && p.namespace == "debian":
		m := debianDistro.FindStringSubmatch(p.qualifiers["distro"])
		if m == nil {
			return ClassUnsupported, nil, "deb purl without a debian-N distro qualifier: the Debian:N stream cannot be selected"
		}
		return ClassQueryable, &Query{Ecosystem: "Debian:" + m[1], Name: name, Version: p.version}, ""
	case p.ptype == "deb" && p.namespace == "ubuntu":
		// Ubuntu keys its OSV streams by the full release, major.minor, unlike
		// Debian's major alone — Ubuntu:20.04, not Ubuntu:20.
		m := ubuntuDistro.FindStringSubmatch(p.qualifiers["distro"])
		if m == nil {
			return ClassUnsupported, nil, "deb purl without an ubuntu-NN.NN distro qualifier: the Ubuntu:NN.NN stream cannot be selected"
		}
		return ClassQueryable, &Query{Ecosystem: "Ubuntu:" + m[1], Name: name, Version: p.version}, ""
	case p.ptype == "rpm" && p.namespace == "redhat":
		m := redhatDistro.FindStringSubmatch(p.qualifiers["distro"])
		if m == nil {
			return ClassUnsupported, nil, "rpm purl without a rhel-N distro qualifier: phase-2 stream confirmation cannot select a Red Hat release, and unconfirmed candidates are cross-stream noise"
		}
		version := p.version
		if epoch := p.qualifiers["epoch"]; epoch != "" {
			// Syft emits the rpm epoch as a qualifier; Red Hat OSV ranges
			// compare full EVR, so an unfolded epoch mismatches wildly.
			version = epoch + ":" + version
		}
		// Binary package names are queried as-is: Red Hat records carry
		// per-binary affected entries. Stream confirmation (phase 2) is the
		// caller's job — 'Red Hat' matches every product stream.
		return ClassQueryable, &Query{Ecosystem: "Red Hat", Name: p.name, Version: version, RedHatMajor: m[1]}, ""
	case p.ptype == "rpm":
		return ClassUnsupported, nil, fmt.Sprintf("no proven OSV mapping for rpm namespace %q", p.namespace)
	case p.ptype == "golang":
		return ClassQueryable, &Query{Ecosystem: "Go", Name: joinName(p.namespace, p.name), Version: p.version}, ""
	case p.ptype == "npm":
		return ClassQueryable, &Query{Ecosystem: "npm", Name: joinName(p.namespace, p.name), Version: p.version}, ""
	case p.ptype == "pypi":
		return ClassQueryable, &Query{Ecosystem: "PyPI", Name: p.name, Version: p.version}, ""
	case p.ptype == "github":
		return ClassUnsupported, nil, "GitHub Actions version matching returns no results via /v1/query (probed against GHSA-mrrh-fwg8-r2c3, 2026-08-06)"
	}
	return ClassUnsupported, nil, fmt.Sprintf("purl type %q is outside the proven OSV mapping table", p.ptype)
}

type purlParts struct {
	ptype      string
	namespace  string
	name       string
	version    string
	qualifiers map[string]string
}

// parsePurl decodes the subset of the purl grammar the corpus exercises:
// pkg:type/namespace.../name@version?qualifiers#subpath, percent-encoded.
func parsePurl(s string) (*purlParts, error) {
	rest, ok := strings.CutPrefix(s, "pkg:")
	if !ok {
		return nil, fmt.Errorf("purl is missing its pkg prefix")
	}
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	qualifiers := map[string]string{}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		for _, kv := range strings.Split(rest[i+1:], "&") {
			k, v, found := strings.Cut(kv, "=")
			if !found || k == "" {
				return nil, fmt.Errorf("malformed qualifier %q", kv)
			}
			decoded, err := url.QueryUnescape(v)
			if err != nil {
				return nil, fmt.Errorf("error unescaping qualifier %s: %w", k, err)
			}
			qualifiers[strings.ToLower(k)] = decoded
		}
		rest = rest[:i]
	}
	version := ""
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		var err error
		if version, err = url.PathUnescape(rest[i+1:]); err != nil {
			return nil, fmt.Errorf("error unescaping version: %w", err)
		}
		rest = rest[:i]
	}
	// A raw '@' surviving the version cut means the purl carried more than
	// one: submitting the mangled remainder would query a different identity.
	if strings.ContainsRune(rest, '@') {
		return nil, fmt.Errorf("purl carries more than one version separator")
	}
	segments := strings.Split(rest, "/")
	if len(segments) < 2 {
		return nil, fmt.Errorf("purl carries no type/name")
	}
	for i, seg := range segments {
		if seg == "" {
			return nil, fmt.Errorf("purl carries an empty segment")
		}
		decoded, err := url.PathUnescape(seg)
		if err != nil {
			return nil, fmt.Errorf("error unescaping segment %q: %w", seg, err)
		}
		// A decoded separator (%2F) would smuggle a different identity past
		// the segment structure.
		if strings.ContainsRune(decoded, '/') {
			return nil, fmt.Errorf("segment %q decodes to a path separator", seg)
		}
		segments[i] = decoded
	}
	return &purlParts{
		ptype:      strings.ToLower(segments[0]),
		namespace:  strings.Join(segments[1:len(segments)-1], "/"),
		name:       segments[len(segments)-1],
		version:    version,
		qualifiers: qualifiers,
	}, nil
}

func joinName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}
