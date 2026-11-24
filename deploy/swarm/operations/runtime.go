package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type cappedBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := (1 << 20) - b.buffer.Len()
	if len(p) > room {
		b.overflow = true
		p = p[:room]
	}
	b.buffer.Write(p)
	return n, nil
}

type commandFailure struct{ missing bool }

func (e *commandFailure) Error() string { return "docker_call_failed" }
func Command(ctx context.Context, budget time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "docker" {
		for _, value := range os.Environ() {
			key := strings.SplitN(value, "=", 2)[0]
			switch key {
			case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_API_VERSION":
				continue
			}
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Official deploy scripts own separately-sessioned Docker children.
		// Give their cancellation traps time to run before bounded escalation.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			return os.ErrProcessDone
		}
		until := time.Now().Add(4 * time.Second)
		for time.Now().Before(until) {
			if syscall.Kill(-cmd.Process.Pid, 0) != nil {
				return nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	var out, stderr cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("command_timeout")
	}
	if out.overflow || stderr.overflow {
		return nil, errors.New("command_output_limit")
	}
	if err != nil {
		missing := false
		var exit *exec.ExitError
		if name == "docker" && errors.As(err, &exit) && exit.ExitCode() == 1 && len(args) > 0 {
			lower := strings.ToLower(stderr.buffer.String())
			target := strings.ToLower(args[len(args)-1])
			for _, pattern := range []string{"no such container: " + target, "no such image: " + target, "no such object: " + target, "no such service: " + target, "no such volume: " + target, "get " + target + ": no such volume", "network " + target + " not found", "service " + target + " not found"} {
				if strings.Contains(lower, pattern) {
					missing = true
				}
			}
		}
		return nil, &commandFailure{missing: missing}
	}
	return bytes.TrimSpace(out.buffer.Bytes()), nil
}
func Docker(ctx context.Context, budget time.Duration, args ...string) ([]byte, error) {
	return Command(ctx, budget, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
}
func Poll(ctx context.Context, budget time.Duration, probe func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		ok, err := probe(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("poll_timeout")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type Resource struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	ID       string `json:"id"`
	OwnerKey string `json:"owner_key"`
	Owner    string `json:"owner"`
}
type Ledger struct {
	Run       string     `json:"run"`
	Resources []Resource `json:"resources"`
}

func SaveLedger(path string, l Ledger) error {
	if !identity.MatchString(l.Run) || l.Run == "pending" {
		return errors.New("invalid_ledger")
	}
	for _, r := range l.Resources {
		if !validResource(r, l.Run) {
			return errors.New("invalid_ledger")
		}
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

var resourceName = regexp.MustCompile(`^[a-zA-Z0-9._:/@-]{1,255}$`)

func volumeIdentity(name, created, mount string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(name+"|"+created+"|"+mount)))
}

func validResource(r Resource, run string) bool {
	switch r.Kind {
	case "container", "service", "network", "volume", "image":
	default:
		return false
	}
	if !resourceName.MatchString(r.Name) || !strings.Contains(r.Name, run) || r.ID != "" && !resourceName.MatchString(r.ID) {
		return false
	}
	return r.OwnerKey == "io.moduleos.operations.run" && r.Owner == run || r.Kind == "service" && r.OwnerKey == "moduleos.app.id" && len(r.Owner) == 36
}
func ReadLedger(path, run string) (Ledger, error) {
	var l Ledger
	if !identity.MatchString(run) || run == "pending" {
		return l, errors.New("invalid_ledger")
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return Ledger{Run: run}, nil
	}
	if err != nil {
		return l, errors.New("ledger_read")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(&l) != nil || d.Decode(new(any)) != io.EOF || l.Run != run {
		return l, errors.New("invalid_ledger")
	}
	for _, r := range l.Resources {
		if !validResource(r, run) {
			return l, errors.New("invalid_ledger")
		}
	}
	return l, nil
}
