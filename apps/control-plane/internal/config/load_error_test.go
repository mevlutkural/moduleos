package config

import (
	"errors"
	"testing"
)

func TestLeadingVariableAcceptsOnlyKnownNames(t *testing.T) {
	for message, want := range map[string]string{
		"PORT must be between 1 and 65535":               "PORT",
		"DOCKER_ENDPOINT and DOCKER_SOCKET must match":   "DOCKER_ENDPOINT",
		"CORS_ALLOWED_ORIGINS: origin at index 0 is bad": "CORS_ALLOWED_ORIGINS",
		"HTTP timeouts must be positive":                 "",
		"sentinel-value must not leak":                   "",
		"":                                               "",
	} {
		if got := leadingVariable(message); got != want {
			t.Fatalf("leadingVariable(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestLoadFailureKeepsTheFirstClassificationAndDropsUnknownVariables(t *testing.T) {
	inner := loadFailure(ReasonValueEmpty, "PORT", errors.New("PORT cannot be explicitly empty"))
	outer := loadFailure(ReasonValueInvalid, "ENV", inner)
	var loadErr *LoadError
	if !errors.As(outer, &loadErr) || loadErr.Reason() != ReasonValueEmpty || loadErr.Variable() != "PORT" {
		t.Fatalf("reclassified error = %#v", outer)
	}
	unknown := loadFailure(ReasonValueInvalid, "sentinel-value", errors.New("invalid"))
	if !errors.As(unknown, &loadErr) || loadErr.Variable() != "" {
		t.Fatalf("unknown variable was kept: %#v", unknown)
	}
	if unknown.Error() != "invalid" {
		t.Fatalf("Error() = %q, want the original message", unknown.Error())
	}
}
