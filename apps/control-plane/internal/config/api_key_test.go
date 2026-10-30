package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAPIKeyReadsSelectedFileExactlyOnce(t *testing.T) {
	calls := 0
	cfg := &Config{Env: "development"}
	environment := map[string]environmentValue{
		"API_KEY":      {},
		"API_KEY_FILE": {value: "/private/secret", set: true},
	}
	reader := func(path string) (string, error) {
		calls++
		if path != "/private/secret" {
			t.Fatalf("path = %q", path)
		}
		return strings.Repeat("a", minimumAPIKeyBytes), nil
	}
	if err := resolveAPIKey(cfg, environment, reader); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || cfg.APIKey != strings.Repeat("a", minimumAPIKeyBytes) {
		t.Fatalf("calls=%d key=%q", calls, cfg.APIKey)
	}
}

func TestResolveAPIKeyDoesNotReadOnConflictOrDirectSource(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		environment map[string]environmentValue
		wantError   string
	}{
		{name: "development direct source", cfg: &Config{Env: "development", APIKey: "direct"}, environment: map[string]environmentValue{"API_KEY": {value: "direct", set: true}}},
		{name: "production direct source", cfg: &Config{Env: "production", APIKey: strings.Repeat("a", 32)}, environment: map[string]environmentValue{"API_KEY": {set: true}}, wantError: "API_KEY"},
		{name: "production missing source", cfg: &Config{Env: "production"}, environment: map[string]environmentValue{}, wantError: "API_KEY_FILE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := resolveAPIKey(test.cfg, test.environment, func(string) (string, error) { calls++; return "", nil })
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v", err)
			}
			if calls != 0 {
				t.Fatalf("reader calls = %d", calls)
			}
		})
	}

	conflict := map[string]environmentValue{"API_KEY": {set: true}, "API_KEY_FILE": {set: true}}
	if err := validateAPIKeySourceConflict(conflict); err == nil {
		t.Fatal("expected source conflict")
	}
}

func TestResolveAPIKeyRejectsInvalidDependencies(t *testing.T) {
	if err := resolveAPIKey(nil, nil, readAPIKeyFile); err == nil {
		t.Fatal("expected nil config rejection")
	}
	if err := resolveAPIKey(&Config{}, nil, nil); err == nil {
		t.Fatal("expected nil reader rejection")
	}
}

func TestResolveAPIKeyPropagatesReaderFailure(t *testing.T) {
	readerError := errors.New("API_KEY_FILE cannot be read")
	cfg := &Config{Env: "development"}
	environment := map[string]environmentValue{"API_KEY_FILE": {value: "/private/key", set: true}}
	if err := resolveAPIKey(cfg, environment, func(string) (string, error) { return "", readerError }); !errors.Is(err, readerError) {
		t.Fatalf("resolve error = %v", err)
	}
}

func TestNormalizeAPIKeyFileContent(t *testing.T) {
	valid := strings.Repeat("a", minimumAPIKeyBytes)
	tests := []struct {
		name      string
		content   string
		want      string
		wantError bool
	}{
		{name: "exact minimum", content: valid, want: valid},
		{name: "single LF", content: valid + "\n", want: valid},
		{name: "single CRLF", content: valid + "\r\n", want: valid},
		{name: "empty", wantError: true},
		{name: "LF only", content: "\n", wantError: true},
		{name: "CRLF only", content: "\r\n", wantError: true},
		{name: "short", content: strings.Repeat("a", minimumAPIKeyBytes-1), wantError: true},
		{name: "leading space", content: " " + valid, wantError: true},
		{name: "trailing space", content: valid + " ", wantError: true},
		{name: "embedded space", content: valid[:16] + " " + valid[16:], wantError: true},
		{name: "tab", content: valid + "\t", wantError: true},
		{name: "embedded LF", content: valid[:16] + "\n" + valid[16:], wantError: true},
		{name: "two trailing LF", content: valid + "\n\n", wantError: true},
		{name: "lone trailing CR", content: valid + "\r", wantError: true},
		{name: "NUL", content: valid + "\x00", wantError: true},
		{name: "vertical tab", content: valid + "\v", wantError: true},
		{name: "Unicode whitespace", content: valid + "\u00a0", wantError: true},
		{name: "Unicode control", content: valid + "\u0085", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := normalizeAPIKeyFileContent([]byte(test.content))
			if test.wantError {
				if err == nil || (test.content != "" && strings.Contains(err.Error(), test.content)) {
					t.Fatalf("value=%q error=%v", value, err)
				}
				return
			}
			if err != nil || value != test.want {
				t.Fatalf("value=%q error=%v want=%q", value, err, test.want)
			}
		})
	}
}

func TestReadAPIKeyFileBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		want      string
		wantError string
	}{
		{name: "minimum", content: strings.Repeat("a", 32), want: strings.Repeat("a", 32)},
		{name: "maximum", content: strings.Repeat("a", maximumAPIKeyFileBytes), want: strings.Repeat("a", maximumAPIKeyFileBytes)},
		{name: "maximum with LF", content: strings.Repeat("a", maximumAPIKeyFileBytes-1) + "\n", want: strings.Repeat("a", maximumAPIKeyFileBytes-1)},
		{name: "oversized", content: strings.Repeat("a", maximumAPIKeyFileBytes+1), wantError: "maximum size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			value, err := readAPIKeyFile(path)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("value length=%d error=%v", len(value), err)
				}
				return
			}
			if err != nil || value != test.want {
				t.Fatalf("value length=%d error=%v want length=%d", len(value), err, len(test.want))
			}
		})
	}
}

func TestReadAPIKeyFileRejectsUnsafePaths(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "private-missing-marker")
	if _, err := readAPIKeyFile(missing); err == nil || strings.Contains(err.Error(), missing) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := readAPIKeyFile(directory); err == nil || !strings.Contains(err.Error(), "regular file") || strings.Contains(err.Error(), directory) {
		t.Fatalf("directory error = %v", err)
	}

	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "private-symlink-marker")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readAPIKeyFile(link); err == nil || !strings.Contains(err.Error(), "regular file") || strings.Contains(err.Error(), link) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestReadAPIKeyFileWithRejectsChangedIdentityAndSafeFailures(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first-private-marker")
	secondPath := filepath.Join(directory, "second-private-marker")
	secret := strings.Repeat("s", 32)
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readAPIKeyFileWith(firstPath, os.Lstat, func(string) (apiKeyFileHandle, error) { return os.Open(secondPath) }); err == nil || !strings.Contains(err.Error(), "changed") || strings.Contains(err.Error(), firstPath) || strings.Contains(err.Error(), secondPath) {
		t.Fatalf("identity error = %v", err)
	}

	privateError := "private-operating-system-marker"
	if _, err := readAPIKeyFileWith(firstPath, os.Lstat, func(string) (apiKeyFileHandle, error) { return nil, errors.New(privateError) }); err == nil || strings.Contains(err.Error(), privateError) || strings.Contains(err.Error(), firstPath) {
		t.Fatalf("open error = %v", err)
	}
	if _, err := readAPIKeyFileWith(firstPath, func(string) (os.FileInfo, error) { return nil, errors.New(privateError) }, nil); err == nil || strings.Contains(err.Error(), privateError) || strings.Contains(err.Error(), firstPath) {
		t.Fatalf("inspect error = %v", err)
	}
	if _, err := readAPIKeyFileWith(firstPath, nil, nil); err == nil {
		t.Fatal("expected nil operation rejection")
	}
}

type overriddenHandle struct {
	*os.File
	info     os.FileInfo
	reader   io.Reader
	readErr  error
	closeErr error
}

func (handle *overriddenHandle) Stat() (os.FileInfo, error) { return handle.info, nil }
func (handle *overriddenHandle) Read(buffer []byte) (int, error) {
	if handle.readErr != nil {
		return 0, handle.readErr
	}
	if handle.reader != nil {
		return handle.reader.Read(buffer)
	}
	return handle.File.Read(buffer)
}
func (handle *overriddenHandle) Close() error {
	err := handle.File.Close()
	if handle.closeErr != nil {
		return handle.closeErr
	}
	return err
}

func TestReadAPIKeyFileWithEnforcesReadBoundAndSafeIOErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-key-marker")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	actualInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	inspector := func(string) (os.FileInfo, error) { return actualInfo, nil }
	opener := func(string) (apiKeyFileHandle, error) {
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		return &overriddenHandle{File: file, info: actualInfo, reader: strings.NewReader(strings.Repeat("a", maximumAPIKeyFileBytes+1))}, nil
	}
	if _, err := readAPIKeyFileWith(path, inspector, opener); err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("bounded read error = %v", err)
	}

	privateError := errors.New("private-read-error-marker")
	readFailure := func(string) (apiKeyFileHandle, error) {
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		return &overriddenHandle{File: file, info: actualInfo, readErr: privateError}, nil
	}
	if _, err := readAPIKeyFileWith(path, inspector, readFailure); err == nil || strings.Contains(err.Error(), privateError.Error()) || strings.Contains(err.Error(), path) {
		t.Fatalf("read error = %v", err)
	}

	shortPath := filepath.Join(t.TempDir(), "valid")
	if err := os.WriteFile(shortPath, []byte(strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	shortInfo, err := os.Lstat(shortPath)
	if err != nil {
		t.Fatal(err)
	}
	closeFailure := func(string) (apiKeyFileHandle, error) {
		file, openErr := os.Open(shortPath)
		if openErr != nil {
			return nil, openErr
		}
		return &overriddenHandle{File: file, info: shortInfo, closeErr: privateError}, nil
	}
	if _, err := readAPIKeyFileWith(shortPath, func(string) (os.FileInfo, error) { return shortInfo, nil }, closeFailure); err == nil || strings.Contains(err.Error(), privateError.Error()) || strings.Contains(err.Error(), shortPath) {
		t.Fatalf("close error = %v", err)
	}
}

func TestReadAPIKeyFileWithRejectsStatFailureSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	privateError := errors.New("private-stat-error-marker")
	handle := &statFailingHandle{Reader: strings.NewReader(strings.Repeat("a", 32)), err: privateError}
	if _, err := readAPIKeyFileWith(path, func(string) (os.FileInfo, error) { return info, nil }, func(string) (apiKeyFileHandle, error) { return handle, nil }); err == nil || strings.Contains(err.Error(), privateError.Error()) || strings.Contains(err.Error(), path) {
		t.Fatalf("stat error = %v", err)
	}
	if !handle.closed {
		t.Fatal("handle was not closed after stat failure")
	}
}

type statFailingHandle struct {
	io.Reader
	err    error
	closed bool
}

func (handle *statFailingHandle) Stat() (os.FileInfo, error) { return nil, handle.err }
func (handle *statFailingHandle) Close() error               { handle.closed = true; return nil }
