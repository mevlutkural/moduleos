package banner

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPrintIncludesDevelopmentRuntimeIdentity(t *testing.T) {
	var output bytes.Buffer
	if err := Print(&output, "v0.1.0", "development", "3000", "moduleos.example"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"███", "version    v0.1.0", "env        development", "port       :3000", "domain     *.moduleos.example"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("banner output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestPrintSuppressesNonDevelopmentOutput(t *testing.T) {
	for _, environment := range []string{"production", "staging", ""} {
		t.Run(environment, func(t *testing.T) {
			writer := failingWriter{err: errors.New("writer must not be used")}
			if err := Print(writer, "v0.1.0", environment, "3000", "moduleos.example"); err != nil {
				t.Fatalf("Print returned an error without writing: %v", err)
			}
			if err := Print(nil, "v0.1.0", environment, "3000", "moduleos.example"); err != nil {
				t.Fatalf("Print with nil output returned %v", err)
			}
		})
	}
}

func TestPrintReturnsDevelopmentWriterFailure(t *testing.T) {
	want := errors.New("write failure")
	if err := Print(failingWriter{err: want}, "v0.1.0", "development", "3000", "moduleos.example"); !errors.Is(err, want) {
		t.Fatalf("Print error = %v, want %v", err, want)
	}
}

func TestPrintRejectsMissingDevelopmentOutput(t *testing.T) {
	if err := Print(nil, "v0.1.0", "development", "3000", "moduleos.example"); !errors.Is(err, ErrMissingOutput) {
		t.Fatalf("Print error = %v, want ErrMissingOutput", err)
	}
	outputs := map[string]io.Writer{
		"pointer":  (*bytes.Buffer)(nil),
		"channel":  nilChannelWriter(nil),
		"function": nilFunctionWriter(nil),
		"map":      nilMapWriter(nil),
		"slice":    nilSliceWriter(nil),
	}
	for name, output := range outputs {
		t.Run(name, func(t *testing.T) {
			if err := Print(output, "v0.1.0", "development", "3000", "moduleos.example"); !errors.Is(err, ErrMissingOutput) {
				t.Fatalf("Print typed-nil error = %v, want ErrMissingOutput", err)
			}
		})
	}
}

type failingWriter struct {
	err error
}

type nilChannelWriter chan byte
type nilFunctionWriter func()
type nilMapWriter map[string]struct{}
type nilSliceWriter []byte

func (nilChannelWriter) Write([]byte) (int, error)  { panic("typed nil writer used") }
func (nilFunctionWriter) Write([]byte) (int, error) { panic("typed nil writer used") }
func (nilMapWriter) Write([]byte) (int, error)      { panic("typed nil writer used") }
func (nilSliceWriter) Write([]byte) (int, error)    { panic("typed nil writer used") }

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
