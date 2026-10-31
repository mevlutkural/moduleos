package config

import (
	"errors"
	"slices"
	"strings"
)

// Stable reasons for a configuration failure. They name a class of problem
// and never carry a configured value, path, or secret.
const (
	ReasonAPIKeySourceConflict = "api_key_source_conflict"
	ReasonAPIKeySourceInvalid  = "api_key_source_invalid"
	ReasonAPIKeyFileInvalid    = "api_key_file_invalid"
	ReasonValueEmpty           = "value_empty"
	ReasonValueUnparsable      = "value_unparsable"
	ReasonValueInvalid         = "value_invalid"
	ReasonValueRequired        = "value_required"
)

// LoadError reports why Load rejected the configuration. Reason and Variable
// are safe to log: Reason is one of the Reason constants and Variable is
// either empty or one of the fixed environment variable names.
type LoadError struct {
	reason   string
	variable string
	err      error
}

func (e *LoadError) Error() string { return e.err.Error() }

func (e *LoadError) Unwrap() error { return e.err }

// Reason returns the stable failure class.
func (e *LoadError) Reason() string { return e.reason }

// Variable returns the environment variable the failure concerns, or "" when
// no single variable is identified.
func (e *LoadError) Variable() string { return e.variable }

func loadFailure(reason, variable string, err error) error {
	var existing *LoadError
	if errors.As(err, &existing) {
		return err
	}
	if !slices.Contains(environmentKeys, variable) {
		variable = ""
	}
	return &LoadError{reason: reason, variable: variable, err: err}
}

// leadingVariable returns the environment variable that starts a validation
// message, when that first word is a known variable name.
func leadingVariable(message string) string {
	word, _, _ := strings.Cut(message, " ")
	word = strings.TrimSuffix(word, ":")
	if slices.Contains(environmentKeys, word) {
		return word
	}
	return ""
}
