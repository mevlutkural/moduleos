package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
)

var (
	// ErrInvalidOptions reports an incomplete or unsafe preparation definition.
	ErrInvalidOptions = errors.New("invalid bootstrap options")
	// ErrOwnershipTransferred reports use of a registrar after handoff or unwind.
	ErrOwnershipTransferred = errors.New("resource ownership transferred")
	// ErrCollaboratorPanic reports a recovered panic at an injected startup seam.
	ErrCollaboratorPanic = errors.New("startup collaborator panicked")
)

// Assembly contains infrastructure-neutral lifecycle collaborators.
type Assembly struct {
	Logger    *slog.Logger
	Server    daemon.Server
	Readiness daemon.Readiness
	Workers   []daemon.Worker
}

// Registrar records cleanup ownership while an assembly is built.
type Registrar interface {
	Add(daemon.Cleanup) error
}

// Builder constructs one assembly and registers every acquired resource.
type Builder func(Registrar) (Assembly, error)

// ListenerFactory synchronously binds the daemon listener.
type ListenerFactory func(network, address string) (net.Listener, error)

// Options defines the complete preparation contract.
type Options struct {
	Build           Builder
	Listen          ListenerFactory
	Network         string
	Address         string
	CleanupTimeout  time.Duration
	ShutdownTimeout time.Duration
}

// Prepare builds, validates, binds, and atomically transfers daemon ownership.
func Prepare(options Options) (*daemon.Daemon, error) {
	if options.Build == nil || options.Listen == nil || strings.TrimSpace(options.Network) == "" || strings.TrimSpace(options.Address) == "" || options.CleanupTimeout <= 0 || options.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("%w: incomplete preparation options", ErrInvalidOptions)
	}

	ledger := &cleanupLedger{}
	assembly, err := callBuilder(options.Build, ledger)
	if err != nil {
		ledger.unwind(options.CleanupTimeout)
		reason := startupReason(err, reasonAssemblyFailed)
		if errors.Is(err, ErrCollaboratorPanic) {
			reason = reasonInternalError
		}
		return nil, safeWrapReason("build assembly failed", err, reason)
	}
	if err := validateAssembly(assembly); err != nil {
		ledger.unwind(options.CleanupTimeout)
		return nil, err
	}

	listener, err := callListener(options.Listen, options.Network, options.Address)
	if err != nil {
		unwindPreparation(listener, ledger, nil, options.CleanupTimeout)
		reason := reasonListenerBindFailed
		if errors.Is(err, ErrCollaboratorPanic) {
			reason = reasonInternalError
		}
		return nil, safeWrapReason("bind listener failed", err, reason)
	}
	if isNilInterface(listener) {
		ledger.unwind(options.CleanupTimeout)
		return nil, fmt.Errorf("%w: listener factory returned nil", ErrInvalidOptions)
	}

	cleanups, err := ledger.transfer()
	if err != nil {
		unwindPreparation(listener, ledger, nil, options.CleanupTimeout)
		return nil, err
	}
	prepared, err := daemon.New(daemon.Options{
		Logger: assembly.Logger, Listener: listener, Server: assembly.Server,
		Readiness: assembly.Readiness, Workers: assembly.Workers, Cleanups: cleanups,
		ShutdownTimeout: options.ShutdownTimeout,
	})
	if err != nil {
		unwindPreparation(listener, nil, cleanups, options.CleanupTimeout)
		return nil, fmt.Errorf("construct daemon: %w", err)
	}
	return prepared, nil
}

func unwindPreparation(listener net.Listener, ledger *cleanupLedger, entries []daemon.Cleanup, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	closeListener(ctx, listener)
	if ledger != nil {
		ledger.unwindContext(ctx)
		return
	}
	unwindContext(entries, ctx)
}

func closeListener(ctx context.Context, listener net.Listener) {
	if isNilInterface(listener) {
		return
	}
	if ctx.Err() != nil {
		return
	}
	done := make(chan struct{}, 1)
	go func() {
		defer func() { _ = recover(); done <- struct{}{} }()
		_ = listener.Close()
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

type cleanupLedger struct {
	mu          sync.Mutex
	entries     []daemon.Cleanup
	names       map[string]struct{}
	transferred bool
	unwound     bool
}

func (ledger *cleanupLedger) Add(cleanup daemon.Cleanup) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.transferred || ledger.unwound {
		return ErrOwnershipTransferred
	}
	if !validName(cleanup.Name) || cleanup.Run == nil {
		return fmt.Errorf("%w: invalid cleanup", ErrInvalidOptions)
	}
	if ledger.names == nil {
		ledger.names = make(map[string]struct{})
	}
	if _, exists := ledger.names[cleanup.Name]; exists {
		return fmt.Errorf("%w: duplicate cleanup", ErrInvalidOptions)
	}
	ledger.names[cleanup.Name] = struct{}{}
	ledger.entries = append(ledger.entries, cleanup)
	return nil
}

func (ledger *cleanupLedger) transfer() ([]daemon.Cleanup, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.transferred || ledger.unwound {
		return nil, ErrOwnershipTransferred
	}
	ledger.transferred = true
	return append([]daemon.Cleanup(nil), ledger.entries...), nil
}

func (ledger *cleanupLedger) unwind(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ledger.unwindContext(ctx)
}

func (ledger *cleanupLedger) unwindContext(ctx context.Context) {
	ledger.mu.Lock()
	if ledger.transferred || ledger.unwound {
		ledger.mu.Unlock()
		return
	}
	ledger.unwound = true
	entries := append([]daemon.Cleanup(nil), ledger.entries...)
	ledger.mu.Unlock()
	unwindContext(entries, ctx)
}

func unwind(entries []daemon.Cleanup, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	unwindContext(entries, ctx)
}

func unwindContext(entries []daemon.Cleanup, ctx context.Context) {
	for index := len(entries) - 1; index >= 0; index-- {
		if ctx.Err() != nil {
			return
		}
		runCleanup(ctx, entries[index])
	}
}

func runCleanup(ctx context.Context, cleanup daemon.Cleanup) {
	defer func() { _ = recover() }()
	_ = cleanup.Run(ctx)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func callBuilder(builder Builder, registrar Registrar) (assembly Assembly, err error) {
	defer func() {
		if recover() != nil {
			assembly = Assembly{}
			err = ErrCollaboratorPanic
		}
	}()
	return builder(registrar)
}

func callListener(factory ListenerFactory, network, address string) (listener net.Listener, err error) {
	defer func() {
		if recover() != nil {
			listener = nil
			err = ErrCollaboratorPanic
		}
	}()
	return factory(network, address)
}

type safeError struct {
	message string
	cause   error
	reason  string
}

func (err *safeError) Error() string { return err.message }

// StartupReason returns the stable reason recorded when the error was wrapped.
func (err *safeError) StartupReason() string { return err.reason }
func (err *safeError) Is(target error) (matched bool) {
	defer func() {
		if recover() != nil {
			matched = false
		}
	}()
	return errors.Is(err.cause, target)
}
func (err *safeError) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, err.message)
}
func (err *safeError) GoString() string { return err.message }

func safeWrapReason(message string, cause error, reason string) error {
	return &safeError{message: message, cause: cause, reason: reason}
}

func validateAssembly(assembly Assembly) error {
	if assembly.Logger == nil || isNilInterface(assembly.Server) || isNilInterface(assembly.Readiness) || len(assembly.Workers) == 0 {
		return fmt.Errorf("%w: incomplete assembly", ErrInvalidOptions)
	}
	names := make(map[string]struct{}, len(assembly.Workers))
	for _, worker := range assembly.Workers {
		if worker.Run == nil || !validName(worker.Name) {
			return fmt.Errorf("%w: invalid worker", ErrInvalidOptions)
		}
		if _, exists := names[worker.Name]; exists {
			return fmt.Errorf("%w: duplicate worker", ErrInvalidOptions)
		}
		names[worker.Name] = struct{}{}
	}
	return nil
}

func validName(name string) bool {
	if name == "" || len(name) > 48 {
		return false
	}
	for _, character := range name {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
