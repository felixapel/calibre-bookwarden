//go:build !(linux && amd64)

package sandbox

import (
	"context"
	"os"
)

// Supported reports whether the sandbox can run here.
func Supported() error { return ErrUnsupported }

// Run refuses: there is no sandbox on this platform.
func Run(context.Context, Spec) ([]byte, error) { return nil, ErrUnsupported }

// Main refuses to launch anything.
func Main() {
	_, _ = os.Stderr.WriteString("bookwarden sandbox: not supported on this platform\n")
	os.Exit(126)
}
