package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
)

var errBuild = errors.New("build failed")

type sensitiveError struct{ value string }

func (err *sensitiveError) Error() string { return err.value }

type panickingMatchError struct{}

func (*panickingMatchError) Error() string { return "private collaborator error" }
func (*panickingMatchError) Is(error) bool { panic("private classification panic") }

type fakeServer struct{}

func (*fakeServer) Serve(net.Listener) error       { return nil }
func (*fakeServer) Shutdown(context.Context) error { return nil }

type fakeReadiness struct{}

func (*fakeReadiness) SetDraining() {}

type nilMapServer map[string]string

func (nilMapServer) Serve(net.Listener) error       { return nil }
func (nilMapServer) Shutdown(context.Context) error { return nil }

type nilSliceReadiness []string

func (nilSliceReadiness) SetDraining() {}

type fakeListener struct{ closes atomic.Int32 }

func (*fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*fakeListener) Addr() net.Addr            { return fakeAddress("test") }
func (listener *fakeListener) Close() error     { listener.closes.Add(1); return nil }

type blockingFakeListener struct {
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (*blockingFakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*blockingFakeListener) Addr() net.Addr            { return fakeAddress("blocking") }
func (listener *blockingFakeListener) Close() error {
	close(listener.entered)
	<-listener.release
	close(listener.done)
	return nil
}

type nilChannelListener chan struct{}

func (nilChannelListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (nilChannelListener) Addr() net.Addr            { return fakeAddress("nil") }
func (nilChannelListener) Close() error              { return nil }

type fakeAddress string

func (address fakeAddress) Network() string { return string(address) }
func (address fakeAddress) String() string  { return string(address) }

func assembly() Assembly {
	return Assembly{
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Server: &fakeServer{}, Readiness: &fakeReadiness{},
		Workers: []daemon.Worker{{Name: "reconciler", Run: func(context.Context) error { return nil }}},
	}
}

func validOptions() Options {
	return Options{
		Build:   func(Registrar) (Assembly, error) { return assembly(), nil },
		Listen:  func(string, string) (net.Listener, error) { return &fakeListener{}, nil },
		Network: "tcp", Address: "127.0.0.1:0",
		CleanupTimeout: time.Second, ShutdownTimeout: time.Second,
	}
}

func TestPrepareRejectsStaticOptionsBeforeBuild(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "nil build", mutate: func(options *Options) { options.Build = nil }},
		{name: "nil listener factory", mutate: func(options *Options) { options.Listen = nil }},
		{name: "blank network", mutate: func(options *Options) { options.Network = " " }},
		{name: "blank address", mutate: func(options *Options) { options.Address = " " }},
		{name: "cleanup timeout", mutate: func(options *Options) { options.CleanupTimeout = 0 }},
		{name: "shutdown timeout", mutate: func(options *Options) { options.ShutdownTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			var built bool
			original := options.Build
			options.Build = func(registrar Registrar) (Assembly, error) { built = true; return original(registrar) }
			test.mutate(&options)
			if _, err := Prepare(options); !errors.Is(err, ErrInvalidOptions) || built {
				t.Fatalf("error=%v built=%v", err, built)
			}
		})
	}
}

func TestPrepareValidatesAssemblyBeforeBinding(t *testing.T) {
	var nilServer *fakeServer
	tests := []struct {
		name   string
		mutate func(*Assembly)
	}{
		{name: "nil logger", mutate: func(value *Assembly) { value.Logger = nil }},
		{name: "typed nil server", mutate: func(value *Assembly) { value.Server = nilServer }},
		{name: "typed nil map server", mutate: func(value *Assembly) { var server nilMapServer; value.Server = server }},
		{name: "nil readiness", mutate: func(value *Assembly) { value.Readiness = nil }},
		{name: "typed nil slice readiness", mutate: func(value *Assembly) { var readiness nilSliceReadiness; value.Readiness = readiness }},
		{name: "no workers", mutate: func(value *Assembly) { value.Workers = nil }},
		{name: "invalid worker", mutate: func(value *Assembly) { value.Workers[0].Name = "Bad" }},
		{name: "long worker", mutate: func(value *Assembly) { value.Workers[0].Name = strings.Repeat("a", 49) }},
		{name: "nil worker", mutate: func(value *Assembly) { value.Workers[0].Run = nil }},
		{name: "duplicate worker", mutate: func(value *Assembly) { value.Workers = append(value.Workers, value.Workers[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			var listened bool
			options.Build = func(Registrar) (Assembly, error) { value := assembly(); test.mutate(&value); return value, nil }
			options.Listen = func(string, string) (net.Listener, error) { listened = true; return &fakeListener{}, nil }
			if _, err := Prepare(options); !errors.Is(err, ErrInvalidOptions) || listened {
				t.Fatalf("error=%v listened=%v", err, listened)
			}
		})
	}
}

func TestPrepareUnwindsFailuresInReverseAndPreservesCause(t *testing.T) {
	var order []string
	options := validOptions()
	options.Build = func(registrar Registrar) (Assembly, error) {
		for _, name := range []string{"first", "second", "third"} {
			name := name
			if err := registrar.Add(daemon.Cleanup{Name: name, Run: func(context.Context) error {
				order = append(order, name)
				if name == "second" {
					panic("ignored")
				}
				return nil
			}}); err != nil {
				t.Fatal(err)
			}
		}
		return Assembly{}, errBuild
	}
	if _, err := Prepare(options); !errors.Is(err, errBuild) {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(order, ",") != "third,second,first" {
		t.Fatalf("order = %v", order)
	}
}

func TestPrepareSanitizesCollaboratorErrorsAndPanicsWhileUnwinding(t *testing.T) {
	secretError := errors.New("secret /host/path docker endpoint")
	tests := []struct {
		name      string
		configure func(*Options, *atomic.Int32)
		want      error
	}{
		{name: "build error", want: secretError, configure: func(options *Options, cleaned *atomic.Int32) {
			options.Build = func(registrar Registrar) (Assembly, error) {
				_ = registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }})
				return Assembly{}, secretError
			}
		}},
		{name: "build panic", want: ErrCollaboratorPanic, configure: func(options *Options, cleaned *atomic.Int32) {
			options.Build = func(registrar Registrar) (Assembly, error) {
				_ = registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }})
				panic("secret panic")
			}
		}},
		{name: "listener error", want: secretError, configure: func(options *Options, cleaned *atomic.Int32) {
			options.Build = func(registrar Registrar) (Assembly, error) {
				_ = registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }})
				return assembly(), nil
			}
			options.Listen = func(string, string) (net.Listener, error) { return nil, secretError }
		}},
		{name: "listener panic", want: ErrCollaboratorPanic, configure: func(options *Options, cleaned *atomic.Int32) {
			options.Build = func(registrar Registrar) (Assembly, error) {
				_ = registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }})
				return assembly(), nil
			}
			options.Listen = func(string, string) (net.Listener, error) { panic("secret panic") }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			var cleaned atomic.Int32
			test.configure(&options, &cleaned)
			_, err := Prepare(options)
			if !errors.Is(err, test.want) || cleaned.Load() != 1 {
				t.Fatalf("error=%v cleaned=%d", err, cleaned.Load())
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/host/path") {
				t.Fatalf("unsafe error text: %v", err)
			}
		})
	}
}

func TestSafeErrorDoesNotExposeRawCollaboratorType(t *testing.T) {
	raw := &sensitiveError{value: "secret collaborator detail"}
	err := safeWrapReason("startup failed", raw, reasonInternalError)
	var recovered *sensitiveError
	diagnostics := fmt.Sprintf("%v %+v %#v %q %s", err, err, err, err, err)
	if !errors.Is(err, raw) || errors.As(err, &recovered) || strings.Contains(err.Error(), raw.value) || strings.Contains(diagnostics, raw.value) || strings.Contains(diagnostics, "sensitiveError") {
		t.Fatalf("unsafe error behavior: error=%v recovered=%v", err, recovered)
	}
}

func TestSafeErrorContainsCollaboratorClassificationPanic(t *testing.T) {
	err := safeWrapReason("startup failed", &panickingMatchError{}, reasonInternalError)
	if errors.Is(err, errBuild) {
		t.Fatal("panicking collaborator error unexpectedly matched")
	}
}

func TestPrepareBindFailureUnwindsWithoutStartingWorkers(t *testing.T) {
	errBind := errors.New("bind failed")
	listener := &fakeListener{}
	var cleaned, workerStarted atomic.Int32
	options := validOptions()
	options.Build = func(registrar Registrar) (Assembly, error) {
		if err := registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }}); err != nil {
			return Assembly{}, err
		}
		value := assembly()
		value.Workers[0].Run = func(context.Context) error { workerStarted.Add(1); return nil }
		return value, nil
	}
	options.Listen = func(string, string) (net.Listener, error) { return listener, errBind }
	if _, err := Prepare(options); !errors.Is(err, errBind) {
		t.Fatalf("error = %v", err)
	}
	if listener.closes.Load() != 1 || cleaned.Load() != 1 || workerStarted.Load() != 0 {
		t.Fatalf("closes=%d cleaned=%d started=%d", listener.closes.Load(), cleaned.Load(), workerStarted.Load())
	}
}

func TestPrepareFailureCleanupSharesOneDeadline(t *testing.T) {
	listener := &blockingFakeListener{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	var cleanupCalls atomic.Int32
	options := validOptions()
	options.CleanupTimeout = 5 * time.Millisecond
	options.Build = func(registrar Registrar) (Assembly, error) {
		if err := registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error {
			cleanupCalls.Add(1)
			return nil
		}}); err != nil {
			return Assembly{}, err
		}
		return assembly(), nil
	}
	options.Listen = func(string, string) (net.Listener, error) {
		return listener, errors.New("bind failed")
	}
	_, err := Prepare(options)
	close(listener.release)
	select {
	case <-listener.done:
	case <-time.After(time.Second):
		t.Fatal("listener close did not finish")
	}
	if err == nil || cleanupCalls.Load() != 0 {
		t.Fatalf("error=%v cleanup calls=%d", err, cleanupCalls.Load())
	}
	select {
	case <-listener.entered:
	default:
		t.Fatal("listener close was not attempted")
	}
}

func TestPrepareRejectsNilListenerAndUnwinds(t *testing.T) {
	for _, listener := range []net.Listener{nil, nilChannelListener(nil)} {
		var cleaned atomic.Int32
		options := validOptions()
		options.Build = func(registrar Registrar) (Assembly, error) {
			_ = registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }})
			return assembly(), nil
		}
		options.Listen = func(string, string) (net.Listener, error) { return listener, nil }
		if _, err := Prepare(options); !errors.Is(err, ErrInvalidOptions) || cleaned.Load() != 1 {
			t.Fatalf("error=%v cleaned=%d", err, cleaned.Load())
		}
	}
}

func TestPrepareConstructorFailureClosesListenerAndCleans(t *testing.T) {
	listener := &fakeListener{}
	var cleaned atomic.Int32
	options := validOptions()
	options.Build = func(registrar Registrar) (Assembly, error) {
		_ = registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { cleaned.Add(1); return nil }})
		value := assembly()
		value.Workers[0].Run = nil
		return value, nil
	}
	options.Listen = func(string, string) (net.Listener, error) { return listener, nil }
	// Assembly validation intentionally catches this before listener allocation.
	if _, err := Prepare(options); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("error=%v", err)
	}
	if listener.closes.Load() != 0 || cleaned.Load() != 1 {
		t.Fatalf("closes=%d cleaned=%d", listener.closes.Load(), cleaned.Load())
	}
}

func TestPrepareTransfersOwnershipAndClosesRegistrar(t *testing.T) {
	options := validOptions()
	var captured Registrar
	options.Build = func(registrar Registrar) (Assembly, error) {
		captured = registrar
		if err := registrar.Add(daemon.Cleanup{Name: "store", Run: func(context.Context) error { return nil }}); err != nil {
			return Assembly{}, err
		}
		return assembly(), nil
	}
	prepared, err := Prepare(options)
	if err != nil || prepared == nil {
		t.Fatalf("prepared=%v error=%v", prepared, err)
	}
	if err := captured.Add(daemon.Cleanup{Name: "late", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrOwnershipTransferred) {
		t.Fatalf("late Add error=%v", err)
	}
}

func TestRegistrarRejectsInvalidDuplicateAndPostUnwindUse(t *testing.T) {
	ledger := &cleanupLedger{}
	if err := ledger.Add(daemon.Cleanup{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("invalid error=%v", err)
	}
	cleanup := daemon.Cleanup{Name: "store", Run: func(context.Context) error { return nil }}
	if err := ledger.Add(cleanup); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Add(cleanup); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("duplicate error=%v", err)
	}
	ledger.unwind(time.Second)
	ledger.unwind(time.Second)
	if err := ledger.Add(daemon.Cleanup{Name: "late", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrOwnershipTransferred) {
		t.Fatalf("late error=%v", err)
	}
	if _, err := ledger.transfer(); !errors.Is(err, ErrOwnershipTransferred) {
		t.Fatalf("transfer error=%v", err)
	}
}

func TestUnwindHonorsSingleDeadline(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	cleanupReturned := make(chan struct{})
	unwindReturned := make(chan struct{})
	var earlier atomic.Int32
	go func() {
		unwind([]daemon.Cleanup{
			{Name: "earlier", Run: func(context.Context) error { earlier.Add(1); return nil }},
			{Name: "blocked", Run: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				close(cleanupReturned)
				return ctx.Err()
			}},
		}, 5*time.Millisecond)
		close(unwindReturned)
	}()
	waitBootstrapSignal(t, started)
	waitBootstrapSignal(t, canceled)
	select {
	case <-unwindReturned:
		t.Fatal("unwind returned before the canceled cleanup joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitBootstrapSignal(t, cleanupReturned)
	waitBootstrapSignal(t, unwindReturned)
	if earlier.Load() != 0 {
		t.Fatalf("cleanup started after deadline: %d", earlier.Load())
	}
}

func waitBootstrapSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bootstrap lifecycle barrier")
	}
}
