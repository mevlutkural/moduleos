package banner

import (
	"errors"
	"fmt"
	"io"
)

var ErrMissingOutput = errors.New("banner output is required in development")

func Print(output io.Writer, version, environment, port, domain string) error {
	if environment != "development" {
		return nil
	}
	if output == nil {
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
