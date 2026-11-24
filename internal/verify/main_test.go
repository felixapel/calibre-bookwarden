package verify_test

import (
	"os"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

// TestMain lets the test binary act as the sandbox launcher, as the
// integration test's OCR re-executes it.
func TestMain(m *testing.M) {
	if sandbox.IsChild(os.Args) {
		sandbox.Main()
	}
	os.Exit(m.Run())
}
