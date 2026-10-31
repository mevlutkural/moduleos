//go:build !(linux && (amd64 || arm64))

package containerinit

import "testing"

func TestNewSystemFailsClosedOnUnsupportedPlatforms(t *testing.T) {
	sys, err := newSystem()
	if sys != nil {
		t.Fatal("unsupported platform returned a system")
	}
	requireCode(t, err, codePlatformUnsupported)
}

func TestMainRejectsUsageAndUnsupportedStart(t *testing.T) {
	if exitCode := Main(nil); exitCode != 2 {
		t.Fatalf("Main(nil) = %d, want 2", exitCode)
	}
	if exitCode := Main([]string{"start"}); exitCode != 1 {
		t.Fatalf("Main(start) = %d, want 1 on an unsupported platform", exitCode)
	}
}
