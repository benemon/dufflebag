package credseal

import "testing"

func TestEnvironmentKeySourcesSealRoundTrip(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name    string
		general string
		alias   string
	}{
		{name: "general key", general: key},
		{name: "Bag Drop alias", alias: key},
		{name: "identical sources", general: key, alias: key},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := ResolveEnvironmentKey(test.general, test.alias, "DFBG_BAGDROP_CREDENTIAL_KEY")
			if err != nil {
				t.Fatal(err)
			}
			sealer := New(nil, resolved)
			sealed, err := sealer.Seal("org", "project", "webhook_secret", "row", "secret")
			if err != nil {
				t.Fatal(err)
			}
			got, err := sealer.Unseal("org", "project", "webhook_secret", "row", sealed)
			if err != nil {
				t.Fatal(err)
			}
			if got != "secret" {
				t.Fatalf("unsealed = %q, want secret", got)
			}
		})
	}
}

func TestResolveEnvironmentKeyRefusesDifferentSources(t *testing.T) {
	_, err := ResolveEnvironmentKey("general", "alias", "DFBG_BAGDROP_CREDENTIAL_KEY")
	if err == nil {
		t.Fatal("ResolveEnvironmentKey succeeded for different sources")
	}
}
