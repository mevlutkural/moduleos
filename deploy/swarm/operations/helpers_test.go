package operations

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandAllowsCancellationTrapToReapSessionChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux setsid cancellation contract")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "reaped")
	script := `setsid sleep 30 & child=$!; trap 'kill -TERM "$child"; wait "$child"; touch "$1"; exit 143' TERM; wait "$child"`
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Command(ctx, time.Second, "sh", "-c", script, "fixture", marker); err == nil {
		t.Fatal("cancellation accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("cancellation trap did not reap child")
	}
}

func TestOfflineCopyPreservesModesDespiteUmask(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if err := os.Chmod(source, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "moduleos.db")
	if err := os.WriteFile(path, []byte("fixture"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	if err := CopyOfflineDirectory(source, target); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{target, 0750}, {filepath.Join(target, "moduleos.db"), 0640}} {
		info, err := os.Stat(item.path)
		if err != nil || info.Mode().Perm() != item.mode {
			t.Fatal("restored mode changed by umask")
		}
	}
}

func TestBoundedCommandAndCopyPaths(t *testing.T) {
	var output cappedBuffer
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", (1<<20)+20))); err != nil {
		t.Fatal(err)
	}
	if !output.overflow || output.buffer.Len() != 1<<20 {
		t.Fatal("ReaderFrom bypassed output cap")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Command(ctx, time.Second, "sh", "-c", "sleep 30"); err == nil {
		t.Fatal("deadline accepted")
	}
	if _, err := Command(context.Background(), time.Second, "sh", "-c", "head -c 1048600 /dev/zero"); err == nil {
		t.Fatal("stdout cap bypassed")
	}
	if _, err := Command(context.Background(), time.Second, "sh", "-c", "head -c 1048600 /dev/zero >&2"); err == nil {
		t.Fatal("stderr cap bypassed")
	}
}

func TestRelayDropsExistingAndRecoversNewConnections(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ops-relay-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "docker.sock")
	upstream, err := net.Listen("unix", target)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	relay, err := NewRelay(filepath.Join(dir, "relay.sock"), target)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	connect := func() net.Conn {
		c, err := net.DialTimeout("unix", filepath.Join(dir, "relay.sock"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		return c
	}
	c := connect()
	defer c.Close()
	if _, err := c.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "before" {
		t.Fatal("forward failed")
	}
	relay.SetAvailable(false)
	if _, err := c.Read(b); err == nil {
		t.Fatal("existing stream survived")
	}
	unavailable := connect()
	defer unavailable.Close()
	unavailable.Write([]byte("closed"))
	if _, err := unavailable.Read(b); err == nil {
		t.Fatal("new connection survived outage")
	}
	relay.SetAvailable(true)
	after := connect()
	defer after.Close()
	after.Write([]byte("after!"))
	if _, err := io.ReadFull(after, b); err != nil || string(b) != "after!" {
		t.Fatal("recovery failed")
	}
}
func TestRelayUnavailableUpstreamAndClose(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ops-relay-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	r, err := NewRelay(filepath.Join(dir, "relay"), filepath.Join(dir, "absent"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", filepath.Join(dir, "relay"))
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("x"))
	b := make([]byte, 1)
	if _, err = c.Read(b); err == nil {
		t.Fatal("missing upstream accepted")
	}
	c.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestEvidenceRejectsUntrustedFields(t *testing.T) {
	e := Event{Run: "abcdef012345", Arch: "amd64", Source: "unknown", Image: "unknown", Stage: "boot", Status: "passed", Reason: "none", ElapsedMS: 1}
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := AppendEvent(path, e); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var out bytes.Buffer
	if err := Summary(bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "```jsonl") {
		t.Fatal("machine-readable evidence missing from summary")
	}
	for _, bad := range []string{`{"stage":"secret","extra":"password"}`, string(data) + `{"unknown":"raw"}`, "", string(data) + "not json"} {
		if Summary(bytes.NewBufferString(bad), io.Discard) == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	e.Reason = "raw-password\n"
	if AppendEvent(path, e) == nil {
		t.Fatal("raw reason accepted")
	}
	e.Reason = "none"
	e.Image = "/host/private"
	if e.Validate() == nil {
		t.Fatal("path accepted as image")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(path, link)
	if AppendEvent(link, e) == nil {
		t.Fatal("symlink evidence accepted")
	}
}
func TestOfflineRestoreCopiesCompanionsAndRejectsUnsafeTargets(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	for _, name := range []string{"moduleos.db", "moduleos.db-wal", "moduleos.db-shm"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := DirectoryHashes(source)
	if err := CopyOfflineDirectory(source, target); err != nil {
		t.Fatal(err)
	}
	after, _ := DirectoryHashes(source)
	restored, _ := DirectoryHashes(target)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(before, restored) {
		t.Fatal("backup changed data")
	}
	if CopyOfflineDirectory(source, source) == nil || CopyOfflineDirectory(source, target) == nil {
		t.Fatal("unsafe restore target accepted")
	}
	other := t.TempDir()
	os.Symlink(filepath.Join(source, "moduleos.db"), filepath.Join(other, "moduleos.db"))
	if CopyOfflineDirectory(other, t.TempDir()) == nil {
		t.Fatal("symlink copied")
	}
	alias := filepath.Join(t.TempDir(), "target")
	os.Symlink(t.TempDir(), alias)
	if CopyOfflineDirectory(source, alias) == nil {
		t.Fatal("symlink target accepted")
	}
	nested := filepath.Join(source, "nested")
	os.Mkdir(nested, 0700)
	if CopyOfflineDirectory(source, nested) == nil {
		t.Fatal("overlapping restore accepted")
	}
}
func TestOfflineDatabaseChecksMigrationAndIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE schema_migrations(version integer, dirty bool)", "INSERT INTO schema_migrations VALUES(12,false)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if err := CheckOfflineDatabase(path, 12); err != nil {
		t.Fatal(err)
	}
	if CheckOfflineDatabase(path, 11) == nil {
		t.Fatal("wrong migration accepted")
	}
	db, _ = sql.Open("sqlite", path)
	db.Exec("UPDATE schema_migrations SET dirty=true")
	db.Close()
	if CheckOfflineDatabase(path, 12) == nil {
		t.Fatal("dirty migration accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"UPDATE schema_migrations SET dirty=false", "CREATE TABLE parent(id integer primary key)", "CREATE TABLE child(parent_id integer references parent(id))", "INSERT INTO child VALUES(99)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if CheckOfflineDatabase(path, 12) == nil {
		t.Fatal("foreign key violation accepted")
	}
}

func TestPollPreservesProbeFailure(t *testing.T) {
	probeFailure := fmt.Errorf("fixture_mismatch")
	if got := Poll(context.Background(), time.Second, func(context.Context) (bool, error) { return false, probeFailure }); got != probeFailure {
		t.Fatal("probe failure misclassified as timeout")
	}
	if got := Poll(context.Background(), time.Millisecond, func(context.Context) (bool, error) { return false, nil }); got == nil || got.Error() != "poll_timeout" {
		t.Fatal("deadline not classified as timeout")
	}
}
