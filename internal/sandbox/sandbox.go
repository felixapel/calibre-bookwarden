// Package sandbox runs the native PDF and OCR tools in a child process that
// cannot reach the network, start processes, trace or mount (spec section 12).
// The child is the bookwarden binary itself, re-executed as a launcher that
// sets rlimits and a seccomp filter and then executes the tool.
package sandbox

import (
	"errors"
	"time"
)

// ChildArg is the hidden first argument that turns the binary into the
// launcher. Callers check IsChild before anything else in main.
const ChildArg = "__bookwarden-sandbox"

// Limits are the child's rlimits. A zero field is left unset.
type Limits struct {
	CPU          time.Duration
	AddressSpace int64
	FileSize     int64
	OpenFiles    uint64
}

// DefaultLimits bound one tool process (ADR-018).
var DefaultLimits = Limits{CPU: 60 * time.Second, AddressSpace: 768 << 20, FileSize: 64 << 20, OpenFiles: 64}

// Spec describes one tool run.
type Spec struct {
	Program   string   // absolute path
	Args      []string // argv[1:]
	Dir       string   // working directory
	Env       []string // the child's whole environment; nil means empty
	Limits    Limits
	MaxStdout int
}

var (
	ErrTimeout        = errors.New("sandbox: the tool ran out of time")
	ErrFailed         = errors.New("sandbox: the tool failed")
	ErrOutputTooLarge = errors.New("sandbox: the tool's output exceeded its limit")
	ErrUnsupported    = errors.New("sandbox: not supported on this platform")
)

// IsChild reports whether args (os.Args) ask for the launcher.
func IsChild(args []string) bool { return len(args) > 1 && args[1] == ChildArg }

// capped keeps at most max bytes and drains the rest, so a chatty child never
// blocks on a full pipe.
type capped struct {
	buf  []byte
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - len(c.buf); len(p) > room {
		c.buf = append(c.buf, p[:max(room, 0)]...)
		c.over = true
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}
