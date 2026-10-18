package banner

import (
	"errors"
	"fmt"
	"io"
	"reflect"
)

var ErrMissingOutput = errors.New("banner output is required in development")

func Print(output io.Writer, version, environment, port, domain string) error {
	if environment != "development" {
		return nil
	}
	if isNilWriter(output) {
		return ErrMissingOutput
	}
	banner := fmt.Sprintf(`
  ███╗   ███╗ ██████╗ ██████╗ ██╗   ██╗██╗     ███████╗ ██████╗ ███████╗
  ████╗ ████║██╔═══██╗██╔══██╗██║   ██║██║     ██╔════╝██╔═══██╗██╔════╝
  ██╔████╔██║██║   ██║██║  ██║██║   ██║██║     █████╗  ██║   ██║███████╗
  ██║╚██╔╝██║██║   ██║██║  ██║██║   ██║██║     ██╔══╝  ██║   ██║╚════██║
  ██║ ╚═╝ ██║╚██████╔╝██████╔╝╚██████╔╝███████╗███████╗╚██████╔╝███████║
  ╚═╝     ╚═╝ ╚═════╝ ╚═════╝  ╚═════╝ ╚══════╝╚══════╝ ╚═════╝ ╚══════╝

  version    %s
  env        %s
  port       :%s
  domain     *.%s

  ─────────────────────────────────────────────────────────────────────

`, version, environment, port, domain)
	_, err := io.WriteString(output, banner)
	return err
}

func isNilWriter(output io.Writer) bool {
	if output == nil {
		return true
	}
	value := reflect.ValueOf(output)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
