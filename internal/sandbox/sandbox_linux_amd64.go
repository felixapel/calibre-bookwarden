package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Supported reports whether the sandbox can run here.
func Supported() error { return nil }

var (
	protectOnce sync.Once
	protectErr  error
)

// protectParent makes bookwarden non-dumpable before its first child. A
// sandboxed tool runs as the same UID; without this it could read
// bookwarden's startup environment (the database URL and the Calibre
// password) from /proc, and, on kernels without Yama such as Unraid's, take
// its open sockets or write its memory. A non-dumpable process needs
// CAP_SYS_PTRACE for all of that, which the container drops. execve makes
// the tool itself dumpable again, so the tools are unaffected.
func protectParent() error {
	protectOnce.Do(func() {
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			protectErr = fmt.Errorf("sandbox: make bookwarden non-dumpable: %w", err)
		}
	})
	return protectErr
}

// Run starts s.Program inside the sandbox and returns its stdout. Stderr is
// discarded: it may echo book text. The child and anything it left behind are
// killed when ctx ends.
func Run(ctx context.Context, s Spec) ([]byte, error) {
	if !filepath.IsAbs(s.Program) {
		return nil, fmt.Errorf("sandbox: program %q is not an absolute path", s.Program)
	}
	if err := protectParent(); err != nil {
		return nil, err
	}
	args := append([]string{ChildArg,
		strconv.FormatInt(int64(s.Limits.CPU/time.Second), 10),
		strconv.FormatInt(s.Limits.AddressSpace, 10),
		strconv.FormatInt(s.Limits.FileSize, 10),
		strconv.FormatUint(s.Limits.OpenFiles, 10),
		s.Program,
	}, s.Args...)
	cmd := exec.CommandContext(ctx, "/proc/self/exe", args...) //nolint:gosec // G204: re-executes this binary; the tool path is checked by the launcher
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	if cmd.Env == nil {
		cmd.Env = []string{} // nil would inherit bookwarden's environment
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out := &capped{max: s.MaxStdout}
	cmd.Stdout = out
	// The parent-death signal fires when the starting thread exits, so the
	// goroutine keeps its thread until the child is gone.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrFailed, err) //nolint:errorlint // the exit status is context, not a cause
	case out.over:
		return nil, ErrOutputTooLarge
	}
	return out.buf, nil
}

// Main is the launcher: it locks itself down and executes the tool. It never
// returns.
func Main() {
	_ = launch(os.Args[2:])
	_, _ = os.Stderr.WriteString("bookwarden sandbox: setup failed\n")
	os.Exit(126)
}

func launch(a []string) error {
	if len(a) < 5 {
		return errors.New("sandbox: missing arguments")
	}
	var n [4]uint64
	for i := range n {
		v, err := strconv.ParseUint(a[i], 10, 64)
		if err != nil {
			return err
		}
		n[i] = v
	}
	cpu, as, fsize, nofile := n[0], n[1], n[2], n[3]
	// No collector from here on: its work could need new mappings after the
	// address-space limit.
	debug.SetGCPercent(-1)
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	limits := []struct {
		res       int
		cur, hard uint64
	}{
		{unix.RLIMIT_CPU, cpu, cpu + 5}, // SIGXCPU, then SIGKILL at the hard limit
		{unix.RLIMIT_FSIZE, fsize, fsize},
		{unix.RLIMIT_NOFILE, nofile, nofile},
	}
	for _, l := range limits {
		if l.cur == 0 {
			continue
		}
		if err := syscall.Setrlimit(l.res, &syscall.Rlimit{Cur: l.cur, Max: l.hard}); err != nil {
			return err
		}
	}
	if err := syscall.Setrlimit(unix.RLIMIT_CORE, &syscall.Rlimit{}); err != nil {
		return err
	}
	plan, err := newExecPlan(a[4], a[4:], os.Environ(), filter(), as)
	if err != nil {
		return err
	}
	return plan.finish(unix.RawSyscall)
}

// execPlan holds what the launcher's last steps pass to the kernel, allocated
// before them. Once the address-space limit is set the Go runtime may be
// unable to map memory, and a new heap arena or thread stack then crashes it,
// so those steps are raw system calls that allocate nothing.
type execPlan struct {
	prog       *unix.SockFprog
	as         *unix.Rlimit // nil: no address-space limit
	path       *byte
	argv, envv []*byte // nil-terminated
}

func newExecPlan(path string, argv, env []string, prog []unix.SockFilter, as uint64) (*execPlan, error) {
	p := &execPlan{prog: &unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}} //nolint:gosec // G115: the program has a few dozen instructions
	if as != 0 {
		p.as = &unix.Rlimit{Cur: as, Max: as}
	}
	var err error
	if p.path, err = unix.BytePtrFromString(path); err != nil {
		return nil, err
	}
	if p.argv, err = syscall.SlicePtrFromStrings(argv); err != nil {
		return nil, err
	}
	if p.envv, err = syscall.SlicePtrFromStrings(env); err != nil {
		return nil, err
	}
	return p, nil
}

// finish installs the filter, sets the address-space limit and executes the
// tool. It returns only if one of them fails.
func (p *execPlan) finish(sys func(trap, a1, a2, a3 uintptr) (uintptr, uintptr, syscall.Errno)) error {
	if err := installFilter(p.prog, sys); err != nil {
		return err
	}
	if p.as != nil {
		if _, _, e := sys(unix.SYS_SETRLIMIT, unix.RLIMIT_AS, uintptr(unsafe.Pointer(p.as)), 0); e != 0 { //nolint:gosec // G103: setrlimit(2) takes a pointer
			return e
		}
	}
	_, _, e := sys(unix.SYS_EXECVE, uintptr(unsafe.Pointer(p.path)), uintptr(unsafe.Pointer(&p.argv[0])), uintptr(unsafe.Pointer(&p.envv[0]))) //nolint:gosec // G103: execve(2) takes pointers
	runtime.KeepAlive(p)
	return e
}

// installFilter loads prog on every thread. With TSYNC, a thread that can't
// be synchronised comes back as a positive return value with no errno, and
// then nothing is installed: that fails the launch too.
func installFilter(prog *unix.SockFprog, sys func(trap, a1, a2, a3 uintptr) (uintptr, uintptr, syscall.Errno)) error {
	r1, _, e := sys(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(prog))) //nolint:gosec // G103: seccomp(2) takes a pointer to the program
	switch {
	case e != 0:
		return e
	case r1 != 0:
		return fmt.Errorf("sandbox: seccomp could not synchronise thread %d", r1)
	}
	return nil
}

// Offsets into struct seccomp_data.
const (
	offNR    = 0
	offArch  = 4
	offArg0  = 16 // low 32 bits on little-endian
	x32Bit   = 0x40000000
	cloneThr = unix.CLONE_THREAD
)

var denied = []uint32{
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_PTRACE, unix.SYS_MOUNT, unix.SYS_FORK, unix.SYS_VFORK,
	// Reaching into another process of the same UID.
	unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD,
	// Kernel interfaces no tool needs, refused even on a host that runs the
	// container without Docker's seccomp profile. Not execve (the filter is
	// installed before the tool is exec'ed) and not kill/tgkill (abort()).
	unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	unix.SYS_BPF, unix.SYS_USERFAULTFD, unix.SYS_PERF_EVENT_OPEN,
}

func filter() []unix.SockFilter {
	ld := func(off uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: off}
	}
	jmp := func(op uint16, k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | op | unix.BPF_K, K: k, Jt: jt, Jf: jf}
	}
	ret := func(k uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: k} }
	errno := func(e syscall.Errno) uint32 { return unix.SECCOMP_RET_ERRNO | uint32(e)&unix.SECCOMP_RET_DATA } //nolint:gosec // G115: errno values are small

	p := []unix.SockFilter{
		ld(offArch),
		jmp(unix.BPF_JEQ, unix.AUDIT_ARCH_X86_64, 1, 0),
		ret(unix.SECCOMP_RET_KILL_PROCESS),
		ld(offNR),
		jmp(unix.BPF_JGE, x32Bit, 0, 1),
		ret(unix.SECCOMP_RET_KILL_PROCESS),
	}
	for _, nr := range denied {
		p = append(p, jmp(unix.BPF_JEQ, nr, 0, 1), ret(errno(unix.EPERM)))
	}
	p = append(p,
		jmp(unix.BPF_JEQ, unix.SYS_CLONE3, 0, 1), ret(errno(unix.ENOSYS)),
		// clone is a new thread only with CLONE_THREAD; anything else would
		// be a new process.
		jmp(unix.BPF_JEQ, unix.SYS_CLONE, 0, 4),
		ld(offArg0),
		jmp(unix.BPF_JSET, cloneThr, 0, 1),
		ret(unix.SECCOMP_RET_ALLOW),
		ret(errno(unix.EPERM)),
		ret(unix.SECCOMP_RET_ALLOW),
	)
	return p
}
