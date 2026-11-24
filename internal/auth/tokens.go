package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

// newToken returns a random 256-bit token and the SHA-256 that gets stored.
func newToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// setupAlphabet leaves out characters that are easy to misread (0/O, 1/I/L, U).
const setupAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// newSetupCode returns a code such as "K7QM-2XWD-9RTA-HP4V" (about 78 bits of
// entropy) and its hash.
func newSetupCode() (code string, hash []byte, err error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(setupAlphabet)))
	for i := range 16 {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", nil, fmt.Errorf("auth: setup code: %w", err)
		}
		b.WriteByte(setupAlphabet[n.Int64()])
	}
	code = b.String()
	return code, hashSetupCode(code), nil
}

// hashSetupCode ignores case, spaces and dashes so a retyped code still matches.
func hashSetupCode(code string) []byte {
	norm := strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToUpper(r)
	}, code)
	sum := sha256.Sum256([]byte(norm))
	return sum[:]
}
