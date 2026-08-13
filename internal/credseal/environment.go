package credseal

import "fmt"

const CredentialKeyEnv = "DFBG_CREDENTIAL_KEY"

// ResolveEnvironmentKey applies the migration rule for a legacy alias.
func ResolveEnvironmentKey(general, alias, aliasName string) (string, error) {
	if general != "" && alias != "" && general != alias {
		return "", fmt.Errorf("%s and %s must be identical when both are set", CredentialKeyEnv, aliasName)
	}
	if general != "" {
		return general, nil
	}
	return alias, nil
}
