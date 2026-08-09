package scan

import "testing"

func TestBandScoreBoundaries(t *testing.T) {
	cases := []struct {
		score float64
		v2    bool
		want  Severity
	}{
		{0, false, SeverityNegligible},
		{0.1, false, SeverityLow},
		{3.9, false, SeverityLow},
		{4.0, false, SeverityMedium},
		{6.9, false, SeverityMedium},
		{7.0, false, SeverityHigh},
		{8.9, false, SeverityHigh},
		{9.0, false, SeverityCritical},
		{10, false, SeverityCritical},
		// CVSS v2 tops out at high: no critical band exists.
		{0, true, SeverityNegligible},
		{3.9, true, SeverityLow},
		{6.9, true, SeverityMedium},
		{7.0, true, SeverityHigh},
		{9.0, true, SeverityHigh},
		{10, true, SeverityHigh},
	}
	for _, tc := range cases {
		if got := bandScore(tc.score, tc.v2); got != tc.want {
			t.Errorf("bandScore(%v, v2=%v) = %q, want %q", tc.score, tc.v2, got, tc.want)
		}
	}
}

func TestLabelAliases(t *testing.T) {
	cases := map[string]Severity{
		"UNKNOWN":    SeverityUnknown,
		"NONE":       SeverityNegligible,
		"NEGLIGIBLE": SeverityNegligible,
		"LOW":        SeverityLow,
		"MODERATE":   SeverityMedium,
		"MEDIUM":     SeverityMedium,
		"IMPORTANT":  SeverityHigh,
		"HIGH":       SeverityHigh,
		"CRITICAL":   SeverityCritical,
		"Critical":   SeverityCritical,
		"low ":       SeverityLow,
	}
	for label, want := range cases {
		got := deriveSeverity([]SeverityValue{{Source: "osv:database_specific", Type: "label", Value: label}})
		if got != want {
			t.Errorf("label %q = %q, want %q", label, got, want)
		}
	}
	// Debian's placeholder urgency carries no signal.
	if got := deriveSeverity([]SeverityValue{{Source: "osv:ecosystem_specific", Type: "urgency", Value: "not yet assigned"}}); got != SeverityUnknown {
		t.Errorf("unusable urgency = %q, want unknown", got)
	}
}

func TestDeriveSeverityWorstWins(t *testing.T) {
	values := []SeverityValue{
		{Source: "osv:database_specific", Type: "label", Value: "LOW"},
		{Source: "osv", Type: "CVSS_V3", Value: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}, // 9.8
	}
	got := deriveSeverity(values)
	if got != SeverityCritical {
		t.Errorf("worst-wins = %q, want critical", got)
	}
	if value := WorstSeverityValue(values); value != values[1].Value {
		t.Errorf("worst verbatim value = %q, want %q", value, values[1].Value)
	}
	if got := deriveSeverity(nil); got != SeverityUnknown {
		t.Errorf("no values = %q, want unknown", got)
	}
	if got := deriveSeverity([]SeverityValue{{Type: "CVSS_V3", Value: "CVSS:3.1/garbage"}}); got != SeverityUnknown {
		t.Errorf("unparseable vector = %q, want unknown", got)
	}
}

// TestCVSSFixtureVectors pins the bands of every vector appearing in the
// captured spike records against published calculator values.
func TestCVSSFixtureVectors(t *testing.T) {
	cases := []struct {
		vector string
		want   Severity
	}{
		// ALPINE-CVE-2022-48174: base score 9.8.
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", SeverityCritical},
		// RHSA-2023:7877: base score 5.9.
		{"CVSS:3.1/AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:N/A:N", SeverityMedium},
		// GHSA-78h2-9frx-2jm8: base score 7.5.
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H", SeverityHigh},
		// DEBIAN-CVE-2023-4527: base score 6.5.
		{"CVSS:3.1/AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:N/A:H", SeverityMedium},
		// CVSS v2 maximal vector scores 10.0 and still bands high.
		{"AV:N/AC:L/Au:N/C:C/I:C/A:C", SeverityHigh},
		// Review finding: some feeds prefix v2 vectors anyway.
		{"CVSS:2.0/AV:N/AC:L/Au:N/C:C/I:C/A:C", SeverityHigh},
		// FIRST CVSS v4.0 example scoring 9.3.
		{"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N", SeverityCritical},
	}
	for _, tc := range cases {
		band, ok := cvssBand(tc.vector)
		if !ok {
			t.Errorf("cvssBand(%q) unusable", tc.vector)
			continue
		}
		if band != tc.want {
			t.Errorf("cvssBand(%q) = %q, want %q", tc.vector, band, tc.want)
		}
	}
}
