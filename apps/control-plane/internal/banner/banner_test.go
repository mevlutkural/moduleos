package banner

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrintIncludesRuntimeIdentity(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = original })

	Print("v0.1.0", "production", "3000", "moduleos.example")
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"███", "version    v0.1.0", "env        production", "port       :3000", "domain     *.moduleos.example"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("banner output does not contain %q:\n%s", want, output)
		}
	}
}
