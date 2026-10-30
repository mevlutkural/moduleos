//go:build darwin || linux

package config

import (
	"os"
	"syscall"
)

func openAPIKeyFile(path string) (apiKeyFileHandle, error) {
	descriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}
