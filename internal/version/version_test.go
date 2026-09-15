package version

import "testing"

func TestString(t *testing.T) {
	originalCommit, originalDate := Commit, Date
	t.Cleanup(func() {
		Commit, Date = originalCommit, originalDate
	})
	Commit = "abc1234"
	Date = "2026-09-15T12:00:00Z"

	want := "go-pdf-forge v0.0.1 (commit abc1234, built 2026-09-15T12:00:00Z)"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
