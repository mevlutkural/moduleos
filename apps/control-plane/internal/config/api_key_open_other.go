//go:build !darwin && !linux

package config

import "errors"

func openAPIKeyFile(string) (apiKeyFileHandle, error) {
	return nil, errors.New("API key files are unsupported on this platform")
}
