// Package version carries go-pdf-forge's build identity. Version is the source
// of truth for releases; Commit and Date are injected by the linker.
package version

import "fmt"

const (
	Name    = "go-pdf-forge"
	Version = "0.0.1"
)

var (
	Commit = "dev"
	Date   = "unknown"
)

func String() string {
	return fmt.Sprintf("%s v%s (commit %s, built %s)", Name, Version, Commit, Date)
}
