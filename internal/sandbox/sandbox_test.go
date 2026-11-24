//go:build linux && amd64

package sandbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/felixapel/calibre-bookwarden/internal/sandbox"
)

func TestMain(m *testing.M) {
	if sandbox.IsChild(os.Args) {
		sandbox.Main()
	}
	if len(os.Args) > 2 && os.Args[1] == "__helper" {
		os.Exit(helper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// errnoOrNil is e as an error, nil for 0.
func errnoOrNil(e syscall.Errno) error {
	if e == 0 {
		return nil
	}
	return e
}

func errName(err error) string {
	var e syscall.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &e):
		return unix.ErrnoName(e)
	default:
		return err.Error()
	}
}

func helper(args []string) int {
	switch args[0] {
	case "echo":
		fmt.Print(args[1])
	case "env":
		fmt.Print(strings.Join(os.Environ(), "\n"))
	case "pwd":
		wd, _ := os.Getwd()
		fmt.Print(wd)
	case "socket":
		_, e1 := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		_, e2 := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		_, e3 := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		fmt.Print(errName(e1), " ", errName(e2), " ", errName(e3))
	case "fork":
		_, err := syscall.ForkExec("/bin/true", []string{"true"}, nil)
		_, _, e := unix.RawSyscall(unix.SYS_CLONE3, 0, 0, 0)
		fmt.Print(errName(err), " ", errName(e))
	case "threads":
		var ready, wg sync.WaitGroup
		release := make(chan struct{})
		for range 8 {
			ready.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				runtime.LockOSThread()
				ready.Done()
				<-release
			}()
		}
		ready.Wait()
		close(release)
		wg.Wait()
		fmt.Print("ok")
	case "trace":
		_, _, e1 := unix.RawSyscall(unix.SYS_PTRACE, unix.PTRACE_TRACEME, 0, 0)
		e2 := unix.Mount("none", os.TempDir(), "tmpfs", 0, "")
		var params [120]byte
		_, _, e3 := unix.RawSyscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&params)), 0)
		fmt.Print(errName(e1), " ", errName(e2), " ", errName(e3))
	case "kernel":
		// Harmless arguments: without the filter these are no-ops, argument
		// errors or, for keyctl, at most the creation of this UID's own user
		// keyrings, so only the filter answers EPERM to all of them.
		calls := []struct {
			nr      uintptr
			a, b, c uintptr
		}{
			{unix.SYS_UNSHARE, 0, 0, 0},
			{unix.SYS_SETNS, ^uintptr(0), 0, 0},
			{unix.SYS_KEYCTL, 0, ^uintptr(2), 0}, // KEYCTL_GET_KEYRING_ID, KEY_SPEC_SESSION_KEYRING
			{unix.SYS_ADD_KEY, 0, 0, 0},
			{unix.SYS_REQUEST_KEY, 0, 0, 0},
			{unix.SYS_BPF, ^uintptr(0), 0, 0},
			{unix.SYS_USERFAULTFD, 0xffff, 0, 0},
			{unix.SYS_PERF_EVENT_OPEN, 0, 0, 0},
			// The mount API with flags no kernel accepts: EINVAL without the
			// filter, whatever the capabilities.
			{unix.SYS_OPEN_TREE, ^uintptr(0), 0, 0xffffffff},
			{unix.SYS_MOUNT_SETATTR, ^uintptr(0), 0, 0xffffffff},
			{unix.SYS_UMOUNT2, 0, 0xffffffff, 0},
		}
		var out []string
		for _, c := range calls {
			_, _, e := unix.RawSyscall(c.nr, c.a, c.b, c.c)
			out = append(out, errName(errnoOrNil(e)))
		}
		// abort() needs tgkill; signal 0 only checks.
		_, _, e := unix.RawSyscall(unix.SYS_TGKILL, uintptr(os.Getpid()), uintptr(unix.Gettid()), 0)
		out = append(out, errName(errnoOrNil(e)))
		fmt.Print(strings.Join(out, " "))
	case "parent":
		// What a compromised tool could take from bookwarden: its startup
		// environment (the secrets), its open files, its memory.
		ppid := os.Getppid()
		_, e1 := os.ReadFile("/proc/" + strconv.Itoa(ppid) + "/environ")
		pidfd, err := unix.PidfdOpen(ppid, 0)
		e2 := err
		if err == nil {
			_, e2 = unix.PidfdGetfd(pidfd, 0, 0)
		}
		buf := make([]byte, 8)
		_, e3 := unix.ProcessVMReadv(ppid, []unix.Iovec{{Base: &buf[0], Len: 8}}, []unix.RemoteIovec{{Base: 0x400000, Len: 8}}, 0)
		fmt.Print(errName(e1), " ", errName(e2), " ", errName(e3))
	case "rlimits":
		var out []string
		for _, r := range []int{unix.RLIMIT_CPU, unix.RLIMIT_AS, unix.RLIMIT_FSIZE, unix.RLIMIT_NOFILE, unix.RLIMIT_CORE} {
			var l unix.Rlimit
			_ = unix.Getrlimit(r, &l)
			out = append(out, strconv.FormatUint(l.Cur, 10))
		}
		fmt.Print(strings.Join(out, " "))
	case "spin":
		for n := 0; ; n++ {
			_ = n
		}
	case "write":
		n, _ := strconv.Atoi(args[1])
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), n))
	case "exit":
		n, _ := strconv.Atoi(args[1])
		return n
	}
	return 0
}

// helperAS is the helper's address-space cap. The helper is a Go program,
// and the Go runtime fails now and then below about 1 GiB (the tools are C).
// The race detector's shadow memory needs terabytes of mappings, so it gets
// no cap at all.
const helperAS = 2 << 30

func limits() sandbox.Limits {
	l := sandbox.DefaultLimits
	l.AddressSpace = helperAS
	if raceEnabled {
		l.AddressSpace = 0
	}
	return l
}

func run(ctx context.Context, t *testing.T, mod func(*sandbox.Spec), args ...string) (string, error) {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	s := sandbox.Spec{Program: self, Args: append([]string{"__helper"}, args...), Dir: t.TempDir(), Limits: limits(), MaxStdout: 1 << 20}
	if mod != nil {
		mod(&s)
	}
	out, err := sandbox.Run(ctx, s)
	return string(out), err
}

func want(t *testing.T, got string, err error, expected string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != expected {
		t.Fatalf("output = %q, want %q", got, expected)
	}
}

func TestRunEcho(t *testing.T) {
	out, err := run(t.Context(), t, nil, "echo", "hi")
	want(t, out, err, "hi")
}

func TestRunEnvironment(t *testing.T) {
	t.Setenv("BOOKWARDEN_SECRET", "x")
	out, err := run(t.Context(), t, func(s *sandbox.Spec) { s.Env = []string{"OMP_THREAD_LIMIT=1"} }, "env")
	want(t, out, err, "OMP_THREAD_LIMIT=1")
	out, err = run(t.Context(), t, nil, "env")
	want(t, out, err, "")
}

func TestRunWorkingDir(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t.Context(), t, func(s *sandbox.Spec) { s.Dir = dir }, "pwd")
	want(t, out, err, dir)
}

func TestRunBlocksNetwork(t *testing.T) {
	out, err := run(t.Context(), t, nil, "socket")
	want(t, out, err, "EPERM EPERM EPERM")
}

func TestRunBlocksProcesses(t *testing.T) {
	out, err := run(t.Context(), t, nil, "fork")
	want(t, out, err, "EPERM ENOSYS")
}

func TestRunAllowsThreads(t *testing.T) {
	out, err := run(t.Context(), t, nil, "threads")
	want(t, out, err, "ok")
}

func TestRunBlocksPtraceMountIOURing(t *testing.T) {
	out, err := run(t.Context(), t, nil, "trace")
	want(t, out, err, "EPERM EPERM EPERM")
}

// TestRunBlocksKernelInterfaces: namespaces, keyrings, BPF, userfaultfd, perf
// events and the mount API are refused with EPERM even without Docker's own seccomp
// profile; tgkill, which abort() uses, still works (execve does too, or the
// helper wouldn't run).
func TestRunBlocksKernelInterfaces(t *testing.T) {
	out, err := run(t.Context(), t, nil, "kernel")
	want(t, out, err, "EPERM EPERM EPERM EPERM EPERM EPERM EPERM EPERM EPERM EPERM EPERM ok")
}

func TestRunCannotReachParent(t *testing.T) {
	out, err := run(t.Context(), t, nil, "parent")
	want(t, out, err, "EACCES EPERM EPERM")
}

func TestRunLimits(t *testing.T) {
	as := strconv.Itoa(helperAS)
	if raceEnabled {
		as = strconv.FormatUint(unix.RLIM_INFINITY, 10)
	}
	out, err := run(t.Context(), t, nil, "rlimits")
	want(t, out, err, "60 "+as+" 67108864 64 0")
}

func TestRunTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := run(ctx, t, nil, "spin")
	if !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("returned after %v", d)
	}
}

func TestRunOutputCap(t *testing.T) {
	_, err := run(t.Context(), t, nil, "write", "2000000")
	if !errors.Is(err, sandbox.ErrOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOutputTooLarge", err)
	}
}

func TestRunFailure(t *testing.T) {
	_, err := run(t.Context(), t, nil, "exit", "3")
	if !errors.Is(err, sandbox.ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
}

func TestRunRelativeProgram(t *testing.T) {
	_, err := sandbox.Run(t.Context(), sandbox.Spec{Program: "tesseract", Dir: t.TempDir(), Limits: limits(), MaxStdout: 10})
	if err == nil {
		t.Fatal("a relative program was accepted")
	}
}

// The whole mount API is denied, including the calls that would answer EPERM
// to an unprivileged tool anyway, so a container run with CAP_SYS_ADMIN
// doesn't open them (review of B9).
func TestDeniedCoversTheMountAPI(t *testing.T) {
	for _, nr := range []uint32{unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_OPEN_TREE, unix.SYS_OPEN_TREE_ATTR,
		unix.SYS_MOVE_MOUNT, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK, unix.SYS_MOUNT_SETATTR} {
		if !slices.Contains(sandbox.Denied(), nr) {
			t.Errorf("syscall %d is not denied", nr)
		}
	}
}
