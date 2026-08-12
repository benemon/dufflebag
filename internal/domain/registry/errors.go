package registry

import "errors"

// The domain has one not-found error. Compatibility adapters map it to whatever
// each endpoint's client demands — code 5 for a missing bucket, code 10 for a
// missing version — because those codes are transport accidents, not domain
// facts. See docs/architecture.md.
var (
	ErrNotFound         = errors.New("not found")
	ErrInvalid          = errors.New("invalid")
	ErrConflict         = errors.New("conflict")
	ErrRestoreInherited = errors.New("restore inherited revocation")
)
