// Package version holds build metadata, stamped in by the Makefile.
package version

// Version and Commit are set with -ldflags -X at build time; a plain
// `go build` leaves the defaults.
var (
	Version = "dev"
	Commit  = ""
)
