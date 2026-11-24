//go:build linux && amd64

package sandbox

import (
	"slices"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestInstallFilterFailsClosed: with TSYNC, seccomp(2) reports a thread it
// couldn't synchronise as a positive return value with no errno, and then
// installs nothing. That must stop the launch.
func TestInstallFilterFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name string
		r1   uintptr
		e    syscall.Errno
		ok   bool
	}{
		{"installed", 0, 0, true},
		{"thread not synchronised", 4242, 0, false},
		{"refused", 0, syscall.EACCES, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sys := func(_, _, _, _ uintptr) (uintptr, uintptr, syscall.Errno) { return tt.r1, 0, tt.e }
			plan, err := newExecPlan("/bin/true", []string{"/bin/true"}, nil, filter(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := installFilter(plan.prog, sys); (err == nil) != tt.ok {
				t.Fatalf("installFilter = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

// TestFinishAllocatesNothing: after the address-space limit the Go runtime
// may be unable to map memory, and a new heap arena or thread stack then
// crashes the launcher (about 1 in 700 launches on the production server). So the
// limit is the last call before execve, and nothing from the filter on
// allocates.
func TestFinishAllocatesNothing(t *testing.T) {
	plan, err := newExecPlan("/bin/true", []string{"/bin/true", "-x"}, []string{"A=1"}, filter(), 768<<20)
	if err != nil {
		t.Fatal(err)
	}
	var traps, firsts, seconds [8]uintptr
	n := 0
	sys := func(trap, a1, a2, _ uintptr) (uintptr, uintptr, syscall.Errno) {
		if n < len(traps) {
			traps[n], firsts[n], seconds[n] = trap, a1, a2
		}
		n++
		return 0, 0, 0
	}
	if allocs := testing.AllocsPerRun(100, func() { n = 0; _ = plan.finish(sys) }); allocs != 0 {
		t.Errorf("finish allocates %v times, want 0", allocs)
	}
	want := []uintptr{unix.SYS_SECCOMP, unix.SYS_SETRLIMIT, unix.SYS_EXECVE}
	if !slices.Equal(traps[:n], want) {
		t.Fatalf("system calls %v, want %v", traps[:n], want)
	}
	if firsts[1] != unix.RLIMIT_AS || seconds[1] != uintptr(unsafe.Pointer(plan.as)) || *plan.as != (unix.Rlimit{Cur: 768 << 20, Max: 768 << 20}) { //nolint:gosec // G103: compares the address finish passed
		t.Errorf("setrlimit(%d, %+v), want RLIMIT_AS at 768 MiB", firsts[1], *plan.as)
	}
}
