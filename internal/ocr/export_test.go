package ocr

import (
	"context"

	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

var (
	PagesToScan  = pagesToScan
	SelfCheckPDF = selfCheckPDF
)

// SetRun replaces the sandbox runner and marks the engine available.
func SetRun(e *Engine, run func(context.Context, sandbox.Spec) ([]byte, error)) {
	e.run = run
	e.available.Store(true)
}

// SetRunOnly replaces the sandbox runner and leaves the engine as it was.
func SetRunOnly(e *Engine, run func(context.Context, sandbox.Spec) ([]byte, error)) {
	e.run = run
}
