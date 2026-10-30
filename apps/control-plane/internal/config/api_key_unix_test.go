//go:build darwin || linux

package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadAPIKeyFileRejectsFIFOAndSocketWithoutOpening(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "moduleos-api-key-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	fifo := filepath.Join(directory, "private-fifo-marker")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAPIKeyFile(fifo); err == nil || !strings.Contains(err.Error(), "regular file") || strings.Contains(err.Error(), fifo) {
		t.Fatalf("FIFO error = %v", err)
	}

	socket := filepath.Join(directory, "private-socket-marker")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close(); _ = os.Remove(socket) })
	if _, err := readAPIKeyFile(socket); err == nil || !strings.Contains(err.Error(), "regular file") || strings.Contains(err.Error(), socket) {
		t.Fatalf("socket error = %v", err)
	}
}

func TestOpenAPIKeyFileReturnsOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if file, err := openAPIKeyFile(path); err == nil || file != nil {
		t.Fatalf("file=%v error=%v", file, err)
	}
}

func TestReadAPIKeyFileRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "moduleos-api-key-race-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}

	inspector := func(value string) (os.FileInfo, error) {
		info, statErr := os.Lstat(value)
		if statErr != nil {
			return nil, statErr
		}
		if removeErr := os.Remove(value); removeErr != nil {
			return nil, removeErr
		}
		if fifoErr := syscall.Mkfifo(value, 0o600); fifoErr != nil {
			return nil, fifoErr
		}
		return info, nil
	}

	done := make(chan error, 1)
	go func() {
		_, readErr := readAPIKeyFileWith(path, inspector, openAPIKeyFile)
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr == nil || !strings.Contains(readErr.Error(), "regular file") || strings.Contains(readErr.Error(), path) {
			t.Fatalf("replacement error = %v", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("API key file open blocked after replacement with FIFO")
	}
}
