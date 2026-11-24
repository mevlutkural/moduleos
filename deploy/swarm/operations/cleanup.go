package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Cleanup diagnostics contain only closed enums, never raw engine errors or
// resource identifiers. The caller remains the guarded, disposable run owner.
type cleanupFailure struct{ kind, stage, reason string }

func (e *cleanupFailure) Error() string {
	return fmt.Sprintf("cleanup kind=%s stage=%s reason=%s", e.kind, e.stage, e.reason)
}

var cleanupKinds = map[string]bool{"guard": true, "ledger": true, "service": true, "container": true, "image": true, "network": true, "volume": true}
var cleanupStages = map[string]bool{"validate": true, "inspect": true, "identity": true, "remove": true, "verify": true}
var cleanupReasons = map[string]bool{"invalid_invocation": true, "unsupported_host": true, "invalid_ledger": true, "guard_read": true, "engine_inspect": true, "engine_identity": true, "client_failed": true, "inspect_failed": true, "invalid_response": true, "owner_mismatch": true, "id_mismatch": true, "remove_failed": true, "verify_timeout": true, "cancelled": true, "deadline": true}

func CleanupDiagnostic(output io.Writer, kind, stage, reason string) {
	if !cleanupKinds[kind] || !cleanupStages[stage] || !cleanupReasons[reason] {
		fmt.Fprintln(output, "cleanup kind=guard stage=validate reason=invalid_invocation")
		return
	}
	fmt.Fprintln(output, (&cleanupFailure{kind, stage, reason}).Error())
}

type cleanupObject struct {
	id     string
	labels map[string]string
}
type cleanupEngine struct{ cli *client.Client }

// Keep the previous CLI capture's response bound when using the typed client.
type cleanupBody struct {
	io.ReadCloser
	remaining int64
	oversized bool
}

func (b *cleanupBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.oversized {
		return 0, errors.New("cleanup_response_limit")
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			b.oversized = true
			return 0, errors.New("cleanup_response_limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
func newCleanupClient(socket string) (*client.Client, error) {
	return client.New(client.WithHost("unix://"+socket), client.WithResponseHook(func(response *http.Response) {
		if response.Body != nil {
			response.Body = &cleanupBody{ReadCloser: response.Body, remaining: 1 << 20, oversized: response.ContentLength > 1<<20}
		}
	}))
}

func (d cleanupEngine) inspect(ctx context.Context, r Resource) (cleanupObject, error) {
	target := r.Name
	if r.ID != "" && r.Kind != "volume" {
		target = r.ID
	}
	switch r.Kind {
	case "service":
		x, err := d.cli.ServiceInspect(ctx, target, client.ServiceInspectOptions{})
		return cleanupObject{x.Service.ID, x.Service.Spec.Labels}, err
	case "container":
		x, err := d.cli.ContainerInspect(ctx, target, client.ContainerInspectOptions{})
		if err != nil {
			return cleanupObject{}, err
		}
		if x.Container.Config == nil {
			return cleanupObject{}, errors.New("invalid_response")
		}
		return cleanupObject{x.Container.ID, x.Container.Config.Labels}, nil
	case "image":
		x, err := d.cli.ImageInspect(ctx, target)
		if err != nil {
			return cleanupObject{}, err
		}
		if x.Config == nil {
			return cleanupObject{}, errors.New("invalid_response")
		}
		return cleanupObject{x.ID, x.Config.Labels}, nil
	case "network":
		x, err := d.cli.NetworkInspect(ctx, target, client.NetworkInspectOptions{})
		return cleanupObject{x.Network.ID, x.Network.Labels}, err
	case "volume":
		x, err := d.cli.VolumeInspect(ctx, target, client.VolumeInspectOptions{})
		if err != nil {
			return cleanupObject{}, err
		}
		if x.Volume.Name != r.Name || x.Volume.CreatedAt == "" || x.Volume.Mountpoint == "" {
			return cleanupObject{}, errors.New("invalid_response")
		}
		return cleanupObject{volumeIdentity(x.Volume.Name, x.Volume.CreatedAt, x.Volume.Mountpoint), x.Volume.Labels}, nil
	default:
		return cleanupObject{}, errors.New("invalid_response")
	}
}
func (d cleanupEngine) remove(ctx context.Context, kind, target string) error {
	switch kind {
	case "service":
		_, err := d.cli.ServiceRemove(ctx, target, client.ServiceRemoveOptions{})
		return err
	case "container":
		_, err := d.cli.ContainerRemove(ctx, target, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		return err
	case "image":
		_, err := d.cli.ImageRemove(ctx, target, client.ImageRemoveOptions{PruneChildren: false})
		return err
	case "network":
		_, err := d.cli.NetworkRemove(ctx, target, client.NetworkRemoveOptions{})
		return err
	case "volume":
		_, err := d.cli.VolumeRemove(ctx, target, client.VolumeRemoveOptions{})
		return err
	default:
		return errors.New("invalid_response")
	}
}

// CleanupLedger uses the existing typed Engine client and HTTP error classes,
// not CLI text. It cannot inherit a remote endpoint or the active CLI context.
func CleanupLedger(ctx context.Context, l Ledger, output io.Writer) error {
	cli, err := newCleanupClient("/var/run/docker.sock")
	if err != nil {
		CleanupDiagnostic(output, "guard", "validate", "client_failed")
		return &cleanupFailure{"guard", "validate", "client_failed"}
	}
	defer cli.Close()
	return cleanupLedger(ctx, l, cleanupEngine{cli}, output)
}

func cleanupLedger(ctx context.Context, l Ledger, engine cleanupEngine, output io.Writer) error {
	if !identity.MatchString(l.Run) || l.Run == "pending" {
		CleanupDiagnostic(output, "ledger", "validate", "invalid_ledger")
		return &cleanupFailure{"ledger", "validate", "invalid_ledger"}
	}
	for _, r := range l.Resources {
		if !validResource(r, l.Run) {
			CleanupDiagnostic(output, "ledger", "validate", "invalid_ledger")
			return &cleanupFailure{"ledger", "validate", "invalid_ledger"}
		}
	}
	var failures []error
	for _, group := range []struct{ kind, owner string }{{"service", "io.moduleos.operations.run"}, {"service", "moduleos.app.id"}, {"container", "io.moduleos.operations.run"}, {"image", "io.moduleos.operations.run"}, {"network", "io.moduleos.operations.run"}, {"volume", "io.moduleos.operations.run"}} {
		for i := len(l.Resources) - 1; i >= 0; i-- {
			r := l.Resources[i]
			if r.Kind != group.kind || r.OwnerKey != group.owner {
				continue
			}
			lastStage, lastReason := "verify", "verify_timeout"
			err := Poll(ctx, 30*time.Second, func(ctx context.Context) (bool, error) {
				probe, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				object, err := engine.inspect(probe, r)
				if errdefs.IsNotFound(err) {
					return true, nil
				}
				if err != nil {
					return false, &cleanupFailure{r.Kind, "inspect", "inspect_failed"}
				}
				if object.id == "" {
					return false, &cleanupFailure{r.Kind, "identity", "invalid_response"}
				}
				if object.labels[r.OwnerKey] != r.Owner {
					return false, &cleanupFailure{r.Kind, "identity", "owner_mismatch"}
				}
				if r.ID != "" && object.id != r.ID {
					return false, &cleanupFailure{r.Kind, "identity", "id_mismatch"}
				}
				target := object.id
				if r.Kind == "volume" {
					target = r.Name
				}
				err = engine.remove(probe, r.Kind, target)
				if err != nil && !errdefs.IsNotFound(err) {
					lastStage, lastReason = "remove", "remove_failed"
				} else {
					lastStage, lastReason = "verify", "verify_timeout"
				}
				return false, nil // An accepted delete is not proof of absence.
			})
			if err != nil {
				var failure *cleanupFailure
				if !errors.As(err, &failure) {
					if errors.Is(ctx.Err(), context.Canceled) {
						lastReason = "cancelled"
					} else if ctx.Err() != nil {
						lastReason = "deadline"
					}
					failure = &cleanupFailure{r.Kind, lastStage, lastReason}
				}
				CleanupDiagnostic(output, failure.kind, failure.stage, failure.reason)
				failures = append(failures, failure)
			}
		}
	}
	return errors.Join(failures...)
}
