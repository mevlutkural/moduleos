//go:build container

// Command fixture is a test-only helper that the container suite copies into
// short-lived containers of the image under test. It prepares volume
// contents, serves a placeholder Docker socket, and reports process identity
// from a shared PID namespace. It is never part of the production image.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: fixture <command> [arguments]")
	}
	arguments := os.Args[2:]
	switch os.Args[1] {
	case "inspect":
		inspect(arguments)
	case "make-legacy":
		requireArguments(arguments, 1)
		makeLegacy(arguments[0])
	case "symlink-sidecar":
		requireArguments(arguments, 1)
		symlinkSidecar(arguments[0])
	case "hardlink-db":
		requireArguments(arguments, 1)
		hardlinkDatabase(arguments[0])
	case "data-root-symlink":
		requireArguments(arguments, 1)
		dataRootSymlink(arguments[0])
	case "regular-file":
		requireArguments(arguments, 1)
		writeFile(arguments[0], 0, 0, 0o600)
	case "serve-socket":
		requireArguments(arguments, 2)
		serveSocket(arguments[0], arguments[1])
	case "proc":
		reportProcess()
	default:
		fail("unknown fixture command")
	}
}

type inspection struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Kind   string `json:"kind,omitempty"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Mode   string `json:"mode,omitempty"`
}

func inspect(paths []string) {
	encoder := json.NewEncoder(os.Stdout)
	for _, path := range paths {
		var stat syscall.Stat_t
		if err := syscall.Lstat(path, &stat); err != nil {
			must(encoder.Encode(inspection{Path: path}))
			continue
		}
		kind := "other"
		switch stat.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			kind = "directory"
		case syscall.S_IFREG:
			kind = "regular"
		case syscall.S_IFLNK:
			kind = "symlink"
		case syscall.S_IFSOCK:
			kind = "socket"
		}
		must(encoder.Encode(inspection{
			Path: path, Exists: true, Kind: kind, UID: int(stat.Uid), GID: int(stat.Gid),
			Mode: fmt.Sprintf("%04o", stat.Mode&0o7777),
		}))
	}
}

// makeLegacy reproduces a data volume restored by root: the directory and
// the SQLite files become root-owned, and unrelated root-owned content is
// added that preparation must never touch.
func makeLegacy(root string) {
	must(os.Chown(root, 0, 0))
	must(os.Chmod(root, 0o755))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := filepath.Join(root, "moduleos.db"+suffix)
		if _, err := os.Lstat(path); err == nil {
			must(os.Chown(path, 0, 0))
			continue
		}
		// A clean stop removes the sidecars; restore them as empty root-owned
		// files, which SQLite accepts, so their adoption is exercised too.
		if suffix != "" {
			writeEmptyFile(path, 0, 0, 0o600)
		}
	}
	writeFile(filepath.Join(root, "unrelated.txt"), 0, 0, 0o600)
	must(os.MkdirAll(filepath.Join(root, "nested"), 0o755))
	must(os.Chown(filepath.Join(root, "nested"), 0, 0))
	writeFile(filepath.Join(root, "nested", "inner.txt"), 0, 0, 0o600)
}

func symlinkSidecar(root string) {
	writeFile(filepath.Join(root, "target.txt"), 0, 0, 0o600)
	must(os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "moduleos.db-shm")))
}

func hardlinkDatabase(root string) {
	writeFile(filepath.Join(root, "other.db"), 0, 0, 0o600)
	must(os.Link(filepath.Join(root, "other.db"), filepath.Join(root, "moduleos.db")))
}

// dataRootSymlink replaces the data root inside a test-owned volume mounted
// at its parent. Docker seeds a fresh volume with the image's directory, so
// the copied, empty data root is removed first.
func dataRootSymlink(parent string) {
	must(os.Remove(filepath.Join(parent, "moduleos")))
	must(os.MkdirAll(filepath.Join(parent, "other"), 0o700))
	must(os.Symlink(filepath.Join(parent, "other"), filepath.Join(parent, "moduleos")))
}

func serveSocket(path, group string) {
	gid, err := strconv.Atoi(group)
	if err != nil {
		fail("invalid group")
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	must(err)
	must(os.Chown(path, 0, gid))
	must(os.Chmod(path, 0o660))
	fmt.Println("ready")
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		// Hold every connection open without answering, like an unresponsive
		// Docker Engine, until the peer closes it.
		go func() {
			buffer := make([]byte, 4096)
			for {
				if _, err := connection.Read(buffer); err != nil {
					_ = connection.Close()
					return
				}
			}
		}()
	}
}

type processReport struct {
	UID        []int    `json:"uid"`
	GID        []int    `json:"gid"`
	Groups     []int    `json:"groups"`
	NoNewPrivs string   `json:"no_new_privs"`
	CapEff     string   `json:"cap_eff"`
	CapPrm     string   `json:"cap_prm"`
	Command    []string `json:"command"`
	Processes  int      `json:"processes"`
}

// reportProcess describes PID 1 of the shared PID namespace, which is the
// daemon container's entrypoint process.
func reportProcess() {
	status, err := os.Open("/proc/1/status")
	must(err)
	defer status.Close()
	var report processReport
	scanner := bufio.NewScanner(status)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		switch key {
		case "Uid":
			report.UID = integers(fields)
		case "Gid":
			report.GID = integers(fields)
		case "Groups":
			report.Groups = integers(fields)
		case "NoNewPrivs":
			report.NoNewPrivs = strings.Join(fields, "")
		case "CapEff":
			report.CapEff = strings.Join(fields, "")
		case "CapPrm":
			report.CapPrm = strings.Join(fields, "")
		}
	}
	must(scanner.Err())
	command, err := os.ReadFile("/proc/1/cmdline")
	must(err)
	report.Command = strings.Split(strings.TrimRight(string(command), "\x00"), "\x00")
	entries, err := os.ReadDir("/proc")
	must(err)
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		// Docker runs the image healthcheck in the same PID namespace; it is
		// not part of the daemon's process tree.
		arguments, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && strings.HasPrefix(string(arguments), "/usr/local/bin/moduleosd-container\x00healthcheck") {
			continue
		}
		report.Processes++
	}
	must(json.NewEncoder(os.Stdout).Encode(report))
}

func integers(fields []string) []int {
	values := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		must(err)
		values = append(values, value)
	}
	return values
}

func writeFile(path string, uid, gid int, mode os.FileMode) {
	must(os.WriteFile(path, []byte("fixture\n"), mode))
	must(os.Chown(path, uid, gid))
}

func writeEmptyFile(path string, uid, gid int, mode os.FileMode) {
	must(os.WriteFile(path, nil, mode))
	must(os.Chown(path, uid, gid))
}

func requireArguments(arguments []string, count int) {
	if len(arguments) != count {
		fail("unexpected argument count")
	}
}

func must(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "fixture:", message)
	os.Exit(1)
}
