package verify_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// TestCalibreReady: a Calibre that answered within the freshness window is
// ready without another call; an older or missing answer is probed now.
func TestCalibreReady(t *testing.T) {
	ctx := context.Background()
	lib := verifytest.NewLibrary("library", books(1, 4, "abcd")...)
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})

	if err := r.CalibreReady(ctx); err != nil {
		t.Fatalf("first CalibreReady with Calibre up: %v", err)
	}
	if st := r.Calibre(); st.State != "ok" {
		t.Fatalf("Calibre() = %+v after the probe", st)
	}

	// Calibre goes away, but it answered a moment ago: no new call is made.
	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	if err := r.CalibreReady(ctx); err != nil {
		t.Fatalf("CalibreReady within the window: %v", err)
	}

	// Past the window the answer is stale, so Calibre is probed now.
	verify.SetCalibreFresh(t, 0)
	verify.SetReadyProbeEvery(t, 0) // the 15 s probe interval has its own tests
	err := r.CalibreReady(ctx)
	if !errors.Is(err, library.ErrUnreachable) {
		t.Fatalf("CalibreReady past the window = %v, want ErrUnreachable", err)
	}
	if st := r.Calibre(); st.State != "unreachable" {
		t.Errorf("Calibre() = %+v, want unreachable", st)
	}

	// And back again.
	lib.SetCheckError(nil)
	if err := r.CalibreReady(ctx); err != nil {
		t.Fatalf("CalibreReady after Calibre returned: %v", err)
	}
}

func TestCalibreReadyHonoursTheContext(t *testing.T) {
	lib := verifytest.NewLibrary("library")
	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.CalibreReady(ctx); err == nil {
		t.Fatal("CalibreReady with Calibre down = nil")
	}
}
