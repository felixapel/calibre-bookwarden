package ocr_test

import (
	"os"
	"testing"

	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

func TestMain(m *testing.M) {
	if sandbox.IsChild(os.Args) {
		sandbox.Main()
	}
	os.Exit(m.Run())
}
