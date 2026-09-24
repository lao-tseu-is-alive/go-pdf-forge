// Package version carries go-pdf-forge's build identity. Version is the source
// of truth for releases; Commit and Date are injected by the linker.
package version

import "fmt"

const (
	// Name is the stable application and release artifact name.
	Name = "go-pdf-forge"
	// Version is the semantic release version and repository source of truth.
	Version = "0.0.8"
)

var (
	// Commit is the source revision injected into release binaries by the linker.
	Commit = "dev"
	// Date is the UTC build timestamp injected into release binaries by the linker.
	Date = "unknown"
)

// String returns the complete human-readable build identity.
func String() string {
	return fmt.Sprintf("%s v%s (commit %s, built %s)", Name, Version, Commit, Date)
}
