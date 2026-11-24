// Package buildinfo carries values stamped in at build time.
package buildinfo

// Version is set with
// -ldflags "-X github.com/felixapel/calibre-bookwarden/internal/buildinfo.Version=…".
var Version = "dev"

// Revision is the commit the binary was built from, set like Version with
// …/internal/buildinfo.Revision=<sha>; "unknown" when it wasn't stamped in.
var Revision = "unknown"
