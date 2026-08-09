package scan

import (
	"strings"

	gocvss20 "github.com/pandatix/go-cvss/20"
	gocvss30 "github.com/pandatix/go-cvss/30"
	gocvss31 "github.com/pandatix/go-cvss/31"
	gocvss40 "github.com/pandatix/go-cvss/40"
)

var severityRank = map[Severity]int{
	SeverityUnknown:    0,
	SeverityNegligible: 1,
	SeverityLow:        2,
	SeverityMedium:     3,
	SeverityHigh:       4,
	SeverityCritical:   5,
}

var labelSeverity = map[string]Severity{
	"unknown":    SeverityUnknown,
	"none":       SeverityNegligible,
	"negligible": SeverityNegligible,
	"low":        SeverityLow,
	"moderate":   SeverityMedium,
	"medium":     SeverityMedium,
	"important":  SeverityHigh,
	"high":       SeverityHigh,
	"critical":   SeverityCritical,
}

// deriveSeverity computes the fixed display scale as the WORST band across
// every supplied value. Values that carry no usable signal (unrecognised
// labels like Debian's "not yet assigned", unparseable vectors) contribute
// nothing; when nothing contributes the result is unknown.
func deriveSeverity(values []SeverityValue) Severity {
	worst := SeverityUnknown
	for _, v := range values {
		band, ok := severityBand(v)
		if !ok {
			continue
		}
		if severityRank[band] > severityRank[worst] {
			worst = band
		}
	}
	return worst
}

// WorstSeverityValue returns the provider's verbatim value for the worst
// normalised severity. It is used by read projections that need both the fixed
// display band and the original provider value.
func WorstSeverityValue(values []SeverityValue) string {
	worst := SeverityUnknown
	value := ""
	for _, v := range values {
		band, ok := severityBand(v)
		if !ok {
			continue
		}
		if value == "" || severityRank[band] > severityRank[worst] {
			worst = band
			value = v.Value
		}
	}
	return value
}

func severityBand(v SeverityValue) (Severity, bool) {
	if strings.HasPrefix(v.Type, "CVSS") {
		return cvssBand(v.Value)
	}
	band, ok := labelSeverity[strings.ToLower(strings.TrimSpace(v.Value))]
	return band, ok
}

func cvssBand(vector string) (Severity, bool) {
	switch {
	case strings.HasPrefix(vector, "CVSS:4.0/"):
		c, err := gocvss40.ParseVector(vector)
		if err != nil {
			return SeverityUnknown, false
		}
		return bandScore(c.Score(), false), true
	case strings.HasPrefix(vector, "CVSS:3.1/"):
		c, err := gocvss31.ParseVector(vector)
		if err != nil {
			return SeverityUnknown, false
		}
		return bandScore(c.BaseScore(), false), true
	case strings.HasPrefix(vector, "CVSS:3.0/"):
		c, err := gocvss30.ParseVector(vector)
		if err != nil {
			return SeverityUnknown, false
		}
		return bandScore(c.BaseScore(), false), true
	default:
		// CVSS v2 vectors usually carry no version header, occasionally
		// parentheses, and some feeds prefix them anyway.
		v2 := strings.Trim(strings.TrimPrefix(vector, "CVSS:2.0/"), "()")
		c, err := gocvss20.ParseVector(v2)
		if err != nil {
			return SeverityUnknown, false
		}
		return bandScore(c.BaseScore(), true), true
	}
}

// bandScore maps a CVSS base score onto the fixed scale. v2 has no critical
// band: its scale tops out at high.
func bandScore(score float64, v2 bool) Severity {
	switch {
	case score <= 0:
		return SeverityNegligible
	case score < 4.0:
		return SeverityLow
	case score < 7.0:
		return SeverityMedium
	case v2:
		return SeverityHigh
	case score < 9.0:
		return SeverityHigh
	default:
		return SeverityCritical
	}
}
