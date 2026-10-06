package swarm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/containerd/errdefs"
	dockerswarm "github.com/moby/moby/api/types/swarm"
)

func TestAttachNetworkMutator(t *testing.T) {
	tests := []struct {
		name        string
		initial     []dockerswarm.NetworkAttachmentConfig
		attachment  NetworkAttachment
		want        []dockerswarm.NetworkAttachmentConfig
		wantChanged bool
	}{
		{
			name:    "Add to empty networks",
			initial: nil,
			attachment: NetworkAttachment{
				Network: "my-net",
				Aliases: []string{"my-alias"},
			},
			want: []dockerswarm.NetworkAttachmentConfig{
				{Target: "my-net", Aliases: []string{"my-alias"}},
			},
			wantChanged: true,
		},
		{
			name: "Add to existing network without alias",
			initial: []dockerswarm.NetworkAttachmentConfig{
				{Target: "my-net", Aliases: []string{"old-alias"}, DriverOpts: map[string]string{"encrypted": "true"}},
			},
			attachment: NetworkAttachment{
				Network: "my-net",
				Aliases: []string{"my-alias"},
			},
			want: []dockerswarm.NetworkAttachmentConfig{
				{Target: "my-net", Aliases: []string{"old-alias", "my-alias"}, DriverOpts: map[string]string{"encrypted": "true"}},
			},
			wantChanged: true,
		},
		{
			name: "Add to existing network with alias already present",
			initial: []dockerswarm.NetworkAttachmentConfig{
				{Target: "my-net", Aliases: []string{"my-alias"}},
			},
			attachment: NetworkAttachment{
				Network: "my-net",
				Aliases: []string{"my-alias"},
			},
			want: []dockerswarm.NetworkAttachmentConfig{
				{Target: "my-net", Aliases: []string{"my-alias"}},
			},
			wantChanged: false,
		},
		{
			name: "Add new network alongside others",
			initial: []dockerswarm.NetworkAttachmentConfig{
				{Target: "other-net", Aliases: []string{"other-alias"}},
			},
			attachment: NetworkAttachment{
				Network: "my-net",
				Aliases: []string{"my-alias"},
			},
			want: []dockerswarm.NetworkAttachmentConfig{
				{Target: "other-net", Aliases: []string{"other-alias"}},
				{Target: "my-net", Aliases: []string{"my-alias"}},
			},
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := attachNetworkMutator(tt.initial, tt.attachment)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMutateServiceNetworksWithRetry_RetriesVersionConflict(t *testing.T) {
	inspectCalls := 0
	updateCalls := 0
	inspect := func(context.Context, string) (dockerswarm.Service, error) {
		inspectCalls++
		return dockerswarm.Service{
			Meta: dockerswarm.Meta{Version: dockerswarm.Version{Index: uint64(inspectCalls)}},
			Spec: dockerswarm.ServiceSpec{TaskTemplate: dockerswarm.TaskSpec{}},
		}, nil
	}
	update := func(_ context.Context, _ string, _ dockerswarm.Version, spec dockerswarm.ServiceSpec) error {
		updateCalls++
		if len(spec.TaskTemplate.Networks) != 1 || spec.TaskTemplate.Networks[0].Target != "network-id" {
			t.Fatalf("mutated networks were not passed to update: %v", spec.TaskTemplate.Networks)
		}
		if updateCalls < 3 {
			return fmt.Errorf("%w: update out of sequence", errdefs.ErrConflict)
		}
		return nil
	}

	err := mutateServiceNetworksWithRetry(context.Background(), "service-id", func(networks []dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool) {
		return append(networks, dockerswarm.NetworkAttachmentConfig{Target: "network-id"}), true
	}, inspect, update)
	if err != nil {
		t.Fatalf("mutateServiceNetworksWithRetry() error = %v", err)
	}
	if inspectCalls != 3 || updateCalls != 3 {
		t.Fatalf("calls inspect/update = %d/%d, want 3/3", inspectCalls, updateCalls)
	}
}

func TestMutateServiceNetworksWithRetry_DoesNotRetryUnrelatedError(t *testing.T) {
	wantErr := errors.New("permission denied")
	inspectCalls := 0
	updateCalls := 0
	inspect := func(context.Context, string) (dockerswarm.Service, error) {
		inspectCalls++
		return dockerswarm.Service{}, nil
	}
	update := func(context.Context, string, dockerswarm.Version, dockerswarm.ServiceSpec) error {
		updateCalls++
		return wantErr
	}

	err := mutateServiceNetworksWithRetry(context.Background(), "service-id", func(networks []dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool) {
		return networks, true
	}, inspect, update)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
	if inspectCalls != 1 || updateCalls != 1 {
		t.Fatalf("unexpected retry: inspect/update calls = %d/%d", inspectCalls, updateCalls)
	}
}

func TestMutateServiceNetworksWithRetry_StopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inspectCalls := 0
	updateCalls := 0
	inspect := func(context.Context, string) (dockerswarm.Service, error) {
		inspectCalls++
		return dockerswarm.Service{}, nil
	}
	update := func(context.Context, string, dockerswarm.Version, dockerswarm.ServiceSpec) error {
		updateCalls++
		cancel()
		return fmt.Errorf("%w: update out of sequence", errdefs.ErrConflict)
	}

	err := mutateServiceNetworksWithRetry(ctx, "service-id", func(networks []dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool) {
		return networks, true
	}, inspect, update)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if inspectCalls != 1 || updateCalls != 1 {
		t.Fatalf("unexpected calls after cancellation: %d/%d", inspectCalls, updateCalls)
	}
}

func TestIsServiceUpdateConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "typed Docker conflict", err: fmt.Errorf("%w: version conflict", errdefs.ErrConflict), want: true},
		{name: "daemon sequence message", err: errors.New("rpc error: update out of sequence"), want: true},
		{name: "case insensitive daemon message", err: errors.New("UPDATE OUT OF SEQUENCE"), want: true},
		{name: "unrelated update error", err: errors.New("service update permission denied"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isServiceUpdateConflict(tt.err); got != tt.want {
				t.Fatalf("isServiceUpdateConflict(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDetachNetworkMutator(t *testing.T) {
	tests := []struct {
		name        string
		initial     []dockerswarm.NetworkAttachmentConfig
		networkName string
		want        []dockerswarm.NetworkAttachmentConfig
		wantChanged bool
	}{
		{
			name: "Remove existing network",
			initial: []dockerswarm.NetworkAttachmentConfig{
				{Target: "my-net", Aliases: []string{"my-alias"}},
				{Target: "other-net", Aliases: []string{"other-alias"}},
			},
			networkName: "my-net",
			want: []dockerswarm.NetworkAttachmentConfig{
				{Target: "other-net", Aliases: []string{"other-alias"}},
			},
			wantChanged: true,
		},
		{
			name: "Remove non-existing network",
			initial: []dockerswarm.NetworkAttachmentConfig{
				{Target: "other-net", Aliases: []string{"other-alias"}},
			},
			networkName: "my-net",
			want: []dockerswarm.NetworkAttachmentConfig{
				{Target: "other-net", Aliases: []string{"other-alias"}},
			},
			wantChanged: false,
		},
		{
			name:        "Remove from empty",
			initial:     nil,
			networkName: "my-net",
			want:        nil,
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := detachNetworkMutator(tt.initial, tt.networkName)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			// reflect.DeepEqual fails if one is nil and other is empty slice but both are semantically empty here.
			// Let's normalize nil to empty slice for comparison or just use length.
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got = %v, want %v", got, tt.want)
			}
		})
	}
}
