// Package migrations exposes the append-only PostgreSQL migrations embedded in
// the application binaries.
package migrations

import "embed"

// FS contains the SQL files in this directory.
//
//go:embed *.sql
var FS embed.FS
