//go:build !(linux && (amd64 || arm64))

package containerinit

func newSystem() (system, error) {
	return nil, fail(codePlatformUnsupported)
}
