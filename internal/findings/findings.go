// Package findings derives versioned observations from sealed file evidence.
// It grants no authority to change Calibre and makes no external requests.
package findings

import "errors"

// Version fixes the local comparison rules. Changing them requires a new
// version and a reader retaining the historical derivation.
const Version = 6

var (
	ErrUnknownVersion  = errors.New("findings: unknown derivation version")
	ErrMismatch        = errors.New("findings: stored findings differ from derivation")
	ErrInvalidEvidence = errors.New("findings: invalid evidence structure")
)
