package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const cleanupTestRun = "abcdef012345"

type cleanupFixture struct {
	mu               sync.Mutex
	objects          map[string]Resource
	removed          []string
	status           int
	deleteStatus     int
	missingMessage   string
	responseID       string
	deferDelete      bool
	deleteRace       bool
	containerVolumes bool
}

func (f *cleanupFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/_ping" {
		w.Header().Set("API-Version", "1.48")
		w.Header().Set("OSType", "linux")
		io.WriteString(w, "OK")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1.48/") {
		http.Error(w, "API version was not negotiated", 400)
		return
	}
	path := regexp.MustCompile(`^/v[0-9.]+`).ReplaceAllString(r.URL.Path, "")
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	if len(parts) != 2 {
		http.Error(w, "unknown fixture route", 500)
		return
	}
	kind := map[string]string{"services": "service", "containers": "container", "images": "image", "networks": "network", "volumes": "volume"}[parts[0]]
	target := strings.TrimSuffix(parts[1], "/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		json.NewEncoder(w).Encode(map[string]string{"message": "untrusted no such volume: " + target})
		return
	}
	key := ""
	var resource Resource
	for k, object := range f.objects {
		if object.Kind == kind && (object.Name == target || object.ID == target) {
			key, resource = k, object
			break
		}
	}
	if key == "" {
		message := f.missingMessage
		if message == "" || kind != "volume" {
			message = kind + " " + target + " not found"
		}
		message = strings.ReplaceAll(message, "TARGET", target)
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"message": message})
		return
	}
	if r.Method == "DELETE" {
		if kind == "image" && (r.URL.Query().Get("noprune") != "1" || r.URL.Query().Get("force") == "1") {
			http.Error(w, "image deletion exceeded owned target", 400)
			return
		}
		if f.deleteRace {
			delete(f.objects, key)
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"message": kind + " disappeared concurrently"})
			return
		}
		if f.deleteStatus != 0 {
			w.WriteHeader(f.deleteStatus)
			json.NewEncoder(w).Encode(map[string]string{"message": "untrusted removal failure"})
			return
		}
		// Explicit fixture dependencies: services and registry container must be
		// removed before their images/network/restore volume can be deleted.
		if kind == "image" || kind == "network" || kind == "volume" {
			for _, other := range f.objects {
				if other.Kind == "service" || other.Kind == "container" {
					w.WriteHeader(409)
					json.NewEncoder(w).Encode(map[string]string{"message": "resource in use"})
					return
				}
			}
		}
		if kind == "container" {
			f.containerVolumes = r.URL.Query().Get("v") == "1" && r.URL.Query().Get("force") == "1"
		}
		f.removed = append(f.removed, kind)
		if !f.deferDelete {
			delete(f.objects, key)
		}
		if kind == "image" {
			io.WriteString(w, "[]")
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "unexpected method", 500)
		return
	}
	labels := map[string]string{resource.OwnerKey: resource.Owner}
	if f.responseID != "" {
		resource.ID = f.responseID
	}
	var object any
	switch kind {
	case "service":
		object = map[string]any{"ID": resource.ID, "Spec": map[string]any{"Name": resource.Name, "Labels": labels}}
	case "image", "container":
		object = map[string]any{"Id": resource.ID, "Name": resource.Name, "Config": map[string]any{"Labels": labels}}
	case "network":
		object = map[string]any{"Id": resource.ID, "Name": resource.Name, "Labels": labels}
	case "volume":
		object = map[string]any{"Name": resource.Name, "CreatedAt": "2026-11-24T00:00:00Z", "Mountpoint": "/fixture/volume", "Labels": labels}
	default:
		http.Error(w, "unsupported fixture kind", 500)
		return
	}
	json.NewEncoder(w).Encode(object)
}

func newCleanupEngineFixture(t *testing.T, f *cleanupFixture) cleanupEngine {
	t.Helper()
	// Short private Unix path, never the real Docker socket or CLI context.
	dir, err := os.MkdirTemp("/tmp", "ops-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: f, ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	cli, err := newCleanupClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return cleanupEngine{cli}
}

func fixtureResource(kind, suffix string) Resource {
	r := Resource{Kind: kind, Name: "ops-" + cleanupTestRun + "-" + suffix, ID: kind + "-id-" + suffix, OwnerKey: "io.moduleos.operations.run", Owner: cleanupTestRun}
	if kind == "volume" {
		r.ID = volumeIdentity(r.Name, "2026-11-24T00:00:00Z", "/fixture/volume")
	}
	return r
}

func TestCleanupAllKindsHistoryAndDependencyOrdering(t *testing.T) {
	for _, missing := range []string{"local", "swarm"} {
		t.Run(missing, func(t *testing.T) {
			f := &cleanupFixture{objects: map[string]Resource{}}
			if missing == "swarm" {
				f.missingMessage = "volume TARGET not found"
			} else {
				f.missingMessage = "get TARGET: no such volume"
			}
			l := Ledger{Run: cleanupTestRun}
			for _, kind := range []string{"volume", "network", "image", "container", "service"} {
				r := fixtureResource(kind, "fixture")
				l.Resources = append(l.Resources, r)
				f.objects[kind] = r
			}
			old := Resource{Kind: "service", Name: "moduleos_app" + cleanupTestRun, OwnerKey: "moduleos.app.id", Owner: "11111111-1111-4111-8111-111111111111"}
			current := old
			current.Owner = "22222222-2222-4222-8222-222222222222"
			l.Resources = append(l.Resources, old, current)
			live := current
			live.ID = "current-service-id"
			f.objects["current"] = live
			// Historical foreign fixture and desired resource, both already gone
			// after the successful black-box journey.
			gone := fixtureResource("service", "foreign")
			l.Resources = append(l.Resources, gone)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var output bytes.Buffer
			if err := cleanupLedger(ctx, l, newCleanupEngineFixture(t, f), &output); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.objects) != 0 || !f.containerVolumes {
				t.Fatal("cleanup left fixtures or failed anonymous volume removal")
			}
			if strings.Join(f.removed, ",") != "service,service,container,image,network,volume" {
				t.Fatalf("dependency order %v", f.removed)
			}
			if output.Len() != 0 {
				t.Fatal("successful cleanup reported failure")
			}
		})
	}
}

func TestCleanupRejectsForeignUnknownAndUnverifiedDeletes(t *testing.T) {
	for _, kind := range []string{"service", "container", "image", "network", "volume"} {
		for _, scenario := range []string{"owner", "identity", "unknown", "malformed", "forbidden", "remove", "not-removed"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				r := fixtureResource(kind, "negative")
				actual := r
				f := &cleanupFixture{objects: map[string]Resource{}}
				switch scenario {
				case "owner":
					actual.Owner = "foreign"
				case "identity":
					if kind == "volume" {
						r.ID = strings.Repeat("a", 64)
					} else {
						f.responseID = "different-immutable-id"
					}
				case "unknown":
					f.status = 500
				case "malformed":
					f.status = 200
				case "forbidden":
					f.status = 403
				case "remove":
					f.deleteStatus = 500
				case "not-removed":
					f.deferDelete = true
				}
				f.objects["subject"] = actual
				budget := time.Second
				if scenario == "remove" || scenario == "not-removed" {
					budget = 150 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				var output bytes.Buffer
				err := cleanupLedger(ctx, Ledger{Run: cleanupTestRun, Resources: []Resource{r}}, newCleanupEngineFixture(t, f), &output)
				if err == nil || !strings.Contains(output.String(), "kind="+kind) {
					t.Fatal("failure not preserved and diagnosed")
				}
				if scenario == "owner" && !strings.Contains(output.String(), "reason=owner_mismatch") {
					t.Fatal("owner refusal was not exercised")
				}
				if scenario == "identity" && !strings.Contains(output.String(), "reason=id_mismatch") {
					t.Fatal("identity refusal was not exercised")
				}
				for _, forbidden := range []string{r.Name, r.ID, "untrusted", "/fixture/volume"} {
					if forbidden != "" && strings.Contains(output.String(), forbidden) {
						t.Fatal("raw runtime value leaked")
					}
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if scenario == "owner" || scenario == "identity" || scenario == "unknown" || scenario == "malformed" || scenario == "forbidden" {
					if len(f.removed) != 0 {
						t.Fatal("unverified resource removed")
					}
				}
			})
		}
	}
}

func TestCleanupHistoricalIntentCannotDeleteDifferentIncarnation(t *testing.T) {
	old := Resource{Kind: "service", Name: "moduleos_app" + cleanupTestRun, OwnerKey: "moduleos.app.id", Owner: "11111111-1111-4111-8111-111111111111"}
	current := old
	current.ID = "new-incarnation"
	current.Owner = "22222222-2222-4222-8222-222222222222"
	f := &cleanupFixture{objects: map[string]Resource{"current": current}}
	var output bytes.Buffer
	if cleanupLedger(context.Background(), Ledger{Run: cleanupTestRun, Resources: []Resource{old}}, newCleanupEngineFixture(t, f), &output) == nil {
		t.Fatal("historical owner adopted a new incarnation")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.removed) != 0 || !strings.Contains(output.String(), "owner_mismatch") {
		t.Fatal("different incarnation was removed")
	}
}

func TestCleanupConcurrentDeleteStillVerifiesAbsence(t *testing.T) {
	r := fixtureResource("volume", "race")
	f := &cleanupFixture{objects: map[string]Resource{"race": r}, deleteRace: true, missingMessage: "volume " + r.Name + " not found"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cleanupLedger(ctx, Ledger{Run: cleanupTestRun, Resources: []Resource{r}}, newCleanupEngineFixture(t, f), io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupTransportFailureIsNotAbsence(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ops-cleanup-missing-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cli, err := newCleanupClient(filepath.Join(dir, "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	var output bytes.Buffer
	r := fixtureResource("network", "missing")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if cleanupLedger(ctx, Ledger{Run: cleanupTestRun, Resources: []Resource{r}}, cleanupEngine{cli}, &output) == nil {
		t.Fatal("transport failure accepted as missing resource")
	}
	if !strings.Contains(output.String(), "inspect_failed") || strings.Contains(output.String(), "missing.sock") {
		t.Fatal("unsafe or absent transport diagnostic")
	}
}

func TestCleanupConcurrentDeletionStillVerifiesAbsence(t *testing.T) {
	for _, kind := range []string{"service", "container", "image", "network", "volume"} {
		t.Run(kind, func(t *testing.T) {
			r := fixtureResource(kind, "race")
			f := &cleanupFixture{objects: map[string]Resource{"subject": r}, deleteRace: true}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := cleanupLedger(ctx, Ledger{Run: cleanupTestRun, Resources: []Resource{r}}, newCleanupEngineFixture(t, f), io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupOldIncarnationCannotRemoveNewOwner(t *testing.T) {
	old := Resource{Kind: "service", Name: "moduleos_app" + cleanupTestRun, OwnerKey: "moduleos.app.id", Owner: "11111111-1111-4111-8111-111111111111"}
	current := old
	current.ID = "new-service-id"
	current.Owner = "22222222-2222-4222-8222-222222222222"
	f := &cleanupFixture{objects: map[string]Resource{"subject": current}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var output bytes.Buffer
	if cleanupLedger(ctx, Ledger{Run: cleanupTestRun, Resources: []Resource{old}}, newCleanupEngineFixture(t, f), &output) == nil {
		t.Fatal("old intent accepted current owner")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.removed) != 0 || !strings.Contains(output.String(), "owner_mismatch") {
		t.Fatal("incarnation owner protection failed")
	}
}

func TestCleanupResponseLimit(t *testing.T) {
	for _, declared := range []bool{false, true} {
		body := &cleanupBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1))), remaining: 1 << 20, oversized: declared}
		data, err := io.ReadAll(body)
		if err == nil || len(data) > 1<<20 {
			t.Fatal("unbounded cleanup response")
		}
		body.Close()
	}
}

func TestCleanupDiagnosticRejectsUntrustedFields(t *testing.T) {
	var output bytes.Buffer
	CleanupDiagnostic(&output, "/private/name", "raw", "environment-value")
	if got := output.String(); got != "cleanup kind=guard stage=validate reason=invalid_invocation\n" {
		t.Fatal(fmt.Sprintf("unexpected safe diagnostic: %q", got))
	}
}

func TestCleanupContinuesOwnedWorkWithoutRemovingForeignService(t *testing.T) {
	service, container := fixtureResource("service", "foreign"), fixtureResource("container", "owned")
	foreign := service
	foreign.Owner = "foreign"
	f := &cleanupFixture{objects: map[string]Resource{"foreign": foreign, "owned": container}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var output bytes.Buffer
	err := cleanupLedger(ctx, Ledger{Run: cleanupTestRun, Resources: []Resource{service, container}}, newCleanupEngineFixture(t, f), &output)
	if err == nil {
		t.Fatal("foreign mismatch ignored")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.removed) != 1 || f.removed[0] != "container" || len(f.objects) != 1 || f.objects["foreign"].Owner != "foreign" {
		t.Fatal("cleanup abandoned owned work or removed foreign resource")
	}
}
