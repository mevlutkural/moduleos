package buildinfo

import "testing"

func TestCurrent(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	Version, Commit, BuildDate = "v0.1.0", "abc123", "2026-08-15T00:00:00Z"
	got := Current()
	if got.Version != Version || got.Commit != Commit || got.BuildDate != BuildDate {
		t.Fatalf("Current() = %#v", got)
	}
}
