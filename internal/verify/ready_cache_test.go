package verify_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felixapel/calibre-bookwarden/internal/library"
	"github.com/felixapel/calibre-bookwarden/internal/verify"
	"github.com/felixapel/calibre-bookwarden/internal/verify/verifytest"
)

// TestCalibreReadyCachesFailuresToo: while Calibre is down, /readyz must not
// turn every request (a monitor, a retry storm) into a probe of Calibre.
func TestCalibreReadyCachesFailuresToo(t *testing.T) {
	verify.SetReadyProbeEvery(t, time.Hour)
	lib := verifytest.NewLibrary("library")
	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})

	for i := range 20 {
		if err := r.CalibreReady(context.Background()); err == nil {
			t.Fatalf("call %d: nil while Calibre is down", i)
		}
	}
	if n := lib.CheckCalls(); n != 1 {
		t.Errorf("20 sequential calls probed Calibre %d times, want 1", n)
	}

	// Concurrent calls on a fresh runner share one probe.
	lib2 := verifytest.NewLibrary("library")
	lib2.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	lib2.SetCheckDelay(50 * time.Millisecond)
	r2 := verify.NewRunner(verifytest.NewMemStore(), lib2, verify.Options{ScratchDir: t.TempDir()})
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() { _ = r2.CalibreReady(context.Background()) })
	}
	wg.Wait()
	if n := lib2.CheckCalls(); n != 1 {
		t.Errorf("30 concurrent calls probed Calibre %d times, want 1", n)
	}
}

func TestCalibreReadyProbesAgainAfterTheInterval(t *testing.T) {
	verify.SetReadyProbeEvery(t, 30*time.Millisecond)
	lib := verifytest.NewLibrary("library")
	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})
	_ = r.CalibreReady(context.Background())
	lib.SetCheckError(nil) // Calibre is back
	time.Sleep(60 * time.Millisecond)
	if err := r.CalibreReady(context.Background()); err != nil {
		t.Fatalf("after the interval, with Calibre back: %v", err)
	}
	if n := lib.CheckCalls(); n != 2 {
		t.Errorf("probes = %d, want 2", n)
	}
}

// A probe cut short by the caller's context says nothing about Calibre and
// isn't remembered.
func TestCalibreReadyDoesNotCacheACancelledProbe(t *testing.T) {
	verify.SetReadyProbeEvery(t, time.Hour)
	lib := verifytest.NewLibrary("library")
	lib.SetCheckError(fmt.Errorf("probe: %w", library.ErrUnreachable))
	r := verify.NewRunner(verifytest.NewMemStore(), lib, verify.Options{ScratchDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = r.CalibreReady(ctx)
	lib.SetCheckError(nil)
	if err := r.CalibreReady(context.Background()); err != nil {
		t.Fatalf("a later call with Calibre up: %v", err)
	}
}
