package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testListener struct {
	closed atomic.Int32
	done   chan struct{}
}

func newTestListener() *testListener                     { return &testListener{done: make(chan struct{})} }
func (listener *testListener) Accept() (net.Conn, error) { <-listener.done; return nil, net.ErrClosed }
func (listener *testListener) Addr() net.Addr            { return testAddress("test") }
func (listener *testListener) Close() error {
	if listener.closed.Add(1) == 1 {
		close(listener.done)
		return nil
	}
	return net.ErrClosed
}

type testAddress string

func (address testAddress) Network() string { return string(address) }
func (address testAddress) String() string  { return string(address) }

type nilOperatorContext struct{}

func (*nilOperatorContext) Deadline() (time.Time, bool) { panic("typed-nil context used") }
func (*nilOperatorContext) Done() <-chan struct{}       { panic("typed-nil context used") }
func (*nilOperatorContext) Err() error                  { panic("typed-nil context used") }
func (*nilOperatorContext) Value(any) any               { panic("typed-nil context used") }

type blockingListener struct {
	entered chan struct{}
	release chan struct{}
}

func (listener *blockingListener) Accept() (net.Conn, error) {
	<-listener.release
	return nil, net.ErrClosed
}
func (listener *blockingListener) Addr() net.Addr { return testAddress("blocking") }
func (listener *blockingListener) Close() error {
	select {
	case <-listener.entered:
	default:
		close(listener.entered)
	}
	<-listener.release
	return nil
}

type testReadiness struct{ drain func() }

func (readiness *testReadiness) SetDraining() { readiness.drain() }

type blockingTerminalHandler struct {
	calls           atomic.Int32
	startupLogged   chan struct{}
	terminalEntered chan struct{}
	releaseTerminal chan struct{}
	terminalDone    chan struct{}
}

func (*blockingTerminalHandler) Enabled(context.Context, slog.Level) bool { return true }
func (handler *blockingTerminalHandler) Handle(context.Context, slog.Record) error {
	if handler.calls.Add(1) == 1 {
		close(handler.startupLogged)
		return nil
	}
	close(handler.terminalEntered)
	<-handler.releaseTerminal
	close(handler.terminalDone)
	return nil
}
func (handler *blockingTerminalHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler *blockingTerminalHandler) WithGroup(string) slog.Handler      { return handler }

type callbackHandler struct {
	calls  atomic.Int32
	handle func(int32)
}

func (*callbackHandler) Enabled(context.Context, slog.Level) bool { return true }
func (handler *callbackHandler) Handle(context.Context, slog.Record) error {
	handler.handle(handler.calls.Add(1))
	return nil
}
func (handler *callbackHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler *callbackHandler) WithGroup(string) slog.Handler      { return handler }

type callbackListener struct{ close func() error }

func (*callbackListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*callbackListener) Addr() net.Addr            { return testAddress("callback") }
func (listener *callbackListener) Close() error     { return listener.close() }

type matchError struct{ match func(error) bool }

func (*matchError) Error() string            { return "classified error" }
func (err *matchError) Is(target error) bool { return err.match(target) }

type testServer struct {
	serve    func(net.Listener) error
	shutdown func(context.Context) error
}

func (server *testServer) Serve(listener net.Listener) error  { return server.serve(listener) }
func (server *testServer) Shutdown(ctx context.Context) error { return server.shutdown(ctx) }

func testLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, nil))
}

func validOptions() Options {
	listener := newTestListener()
	return Options{
		Logger: testLogger(&bytes.Buffer{}), Listener: listener,
		Server: &testServer{
			serve:    func(net.Listener) error { <-listener.done; return nil },
			shutdown: func(context.Context) error { return listener.Close() },
		},
		Readiness:       &testReadiness{drain: func() {}},
		Workers:         []Worker{{Name: "reconciler", Run: func(ctx context.Context) error { <-ctx.Done(); return nil }}},
		ShutdownTimeout: time.Second,
	}
}

func TestNewValidatesAndCopiesOptions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "nil logger", mutate: func(options *Options) { options.Logger = nil }},
		{name: "nil listener", mutate: func(options *Options) { options.Listener = nil }},
		{name: "nil server", mutate: func(options *Options) { options.Server = nil }},
		{name: "typed nil server", mutate: func(options *Options) { var server *testServer; options.Server = server }},
		{name: "nil readiness", mutate: func(options *Options) { options.Readiness = nil }},
		{name: "typed nil readiness", mutate: func(options *Options) { var readiness *testReadiness; options.Readiness = readiness }},
		{name: "zero timeout", mutate: func(options *Options) { options.ShutdownTimeout = 0 }},
		{name: "no workers", mutate: func(options *Options) { options.Workers = nil }},
		{name: "empty worker", mutate: func(options *Options) { options.Workers[0].Name = "" }},
		{name: "long worker", mutate: func(options *Options) { options.Workers[0].Name = strings.Repeat("a", maxComponentNameLength+1) }},
		{name: "invalid worker", mutate: func(options *Options) { options.Workers[0].Name = "Bad.Name" }},
		{name: "nil worker", mutate: func(options *Options) { options.Workers[0].Run = nil }},
		{name: "duplicate worker", mutate: func(options *Options) { options.Workers = append(options.Workers, options.Workers[0]) }},
		{name: "invalid cleanup", mutate: func(options *Options) {
			options.Cleanups = []Cleanup{{Name: "bad/name", Run: func(context.Context) error { return nil }}}
		}},
		{name: "nil cleanup", mutate: func(options *Options) { options.Cleanups = []Cleanup{{Name: "store"}} }},
		{name: "duplicate cleanup", mutate: func(options *Options) {
			cleanup := Cleanup{Name: "store", Run: func(context.Context) error { return nil }}
			options.Cleanups = []Cleanup{cleanup, cleanup}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			test.mutate(&options)
			if _, err := New(options); !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}

	options := validOptions()
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Workers[0].Name = "changed"
	options.Cleanups = append(options.Cleanups, Cleanup{Name: "late", Run: func(context.Context) error { return nil }})
	if daemon.workers[0].Name != "reconciler" || len(daemon.cleanups) != 0 {
		t.Fatal("constructor did not defensively copy slices")
	}
}

func TestRunCleanShutdownOrderAndOneShot(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) { mu.Lock(); events = append(events, event); mu.Unlock() }
	listener := newTestListener()
	serverRelease := make(chan struct{})
	serverStarted := make(chan struct{})
	options := validOptions()
	options.Listener = listener
	options.Readiness = &testReadiness{drain: func() { record("drain") }}
	options.Server = &testServer{
		serve: func(net.Listener) error {
			record("serve")
			close(serverStarted)
			<-serverRelease
			record("server-joined")
			return nil
		},
		shutdown: func(context.Context) error { record("shutdown"); close(serverRelease); return nil },
	}
	options.Workers = []Worker{
		{Name: "reconciler", Run: func(ctx context.Context) error {
			record("worker-1")
			<-ctx.Done()
			record("worker-1-stopped")
			return nil
		}},
		{Name: "watcher", Run: func(ctx context.Context) error {
			record("worker-2")
			<-ctx.Done()
			record("worker-2-stopped")
			return nil
		}},
	}
	options.Cleanups = []Cleanup{
		{Name: "database", Run: func(context.Context) error { record("cleanup-database"); return nil }},
		{Name: "docker", Run: func(context.Context) error { record("cleanup-docker"); return nil }},
	}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome, 1)
	go func() { done <- daemon.Run(ctx) }()
	waitSignal(t, serverStarted)
	cancel()
	outcome := receiveOutcome(t, done)
	if outcome.ExitCode != 0 || outcome.Cause != CauseOperator {
		t.Fatalf("outcome = %+v", outcome)
	}
	wantOrder(t, events, "drain", "shutdown", "server-joined", "worker-1-stopped", "cleanup-docker", "cleanup-database")
	if listener.closed.Load() != 1 {
		t.Fatalf("listener close attempts = %d", listener.closed.Load())
	}
	if reused := daemon.Run(nil); reused.ExitCode != 1 || reused.Cause != CauseInvalid {
		t.Fatalf("reused = %+v", reused)
	}
}

func TestRequestedHTTPServerCloseIsClean(t *testing.T) {
	listener := newTestListener()
	serverStarted := make(chan struct{})
	options := validOptions()
	options.Listener = listener
	options.Server = &testServer{
		serve: func(bound net.Listener) error {
			close(serverStarted)
			_, _ = bound.Accept()
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error { return listener.Close() },
	}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan Outcome, 1)
	go func() { outcomes <- daemon.Run(ctx) }()
	waitSignal(t, serverStarted)
	cancel()
	outcome := receiveOutcome(t, outcomes)
	if outcome.ExitCode != 0 || outcome.ShutdownFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestServerReturnBeforeShutdownInitiationIsFatal(t *testing.T) {
	options := validOptions()
	options.Server = &testServer{
		serve:    func(net.Listener) error { return nil },
		shutdown: func(context.Context) error { return nil },
	}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	results := make(chan componentResult, 1)
	group := &sync.WaitGroup{}
	group.Add(1)
	daemon.runServer(&completionGate{}, started, newTerminalArbiter(), results, group)
	group.Wait()
	result := <-results
	if !result.unexpected || result.kind != CauseServer {
		t.Fatalf("result = %+v", result)
	}
}

func TestTerminalArbiterRetainsFirstArrival(t *testing.T) {
	arbiter := newTerminalArbiter()
	arbiter.report(componentResult{kind: CauseWorker, component: "reconciler"})
	arbiter.report(componentResult{kind: CauseOperator})
	result := <-arbiter.first
	if result.kind != CauseWorker || result.component != "reconciler" {
		t.Fatalf("result = %+v", result)
	}
}

func TestTerminalArbiterSerializesStartupDecision(t *testing.T) {
	terminated := newTerminalArbiter()
	terminated.report(componentResult{kind: CauseWorker, component: "reconciler"})
	var serveCalls atomic.Int32
	if terminated.beginStartup(func() { serveCalls.Add(1) }) {
		t.Fatal("startup began after termination")
	}
	if serveCalls.Load() != 0 {
		t.Fatal("server launch ran after termination")
	}

	running := newTerminalArbiter()
	launchEntered := make(chan struct{})
	launchLockHeld := make(chan bool, 1)
	releaseLaunch := make(chan struct{})
	startupDone := make(chan bool, 1)
	go func() {
		startupDone <- running.beginStartup(func() {
			serveCalls.Add(1)
			if running.mu.TryLock() {
				running.mu.Unlock()
				launchLockHeld <- false
			} else {
				launchLockHeld <- true
			}
			close(launchEntered)
			<-releaseLaunch
		})
	}()
	waitSignal(t, launchEntered)
	if !<-launchLockHeld {
		t.Fatal("startup launch ran outside the terminal arbitration lock")
	}
	reportAttempted := make(chan struct{})
	reportDone := make(chan struct{})
	go func() {
		close(reportAttempted)
		running.report(componentResult{kind: CauseOperator})
		close(reportDone)
	}()
	waitSignal(t, reportAttempted)
	select {
	case <-reportDone:
		t.Fatal("terminal report interleaved with the atomic startup decision")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseLaunch)
	if !<-startupDone {
		t.Fatal("startup was rejected before termination")
	}
	waitSignal(t, reportDone)
	if serveCalls.Load() != 1 {
		t.Fatalf("server launch calls = %d", serveCalls.Load())
	}
	if trigger := running.poll(); trigger == nil || trigger.kind != CauseOperator {
		t.Fatalf("trigger = %+v", trigger)
	}
}

func TestCompletionGateSerializesReturnAndStopRequest(t *testing.T) {
	completed := &completionGate{}
	if !completed.complete() {
		t.Fatal("active component was not classified as unexpected")
	}
	completed.requestStop()
	if completed.state.Load() != componentCompleted {
		t.Fatalf("completed state = %d", completed.state.Load())
	}

	stopping := &completionGate{}
	stopping.requestStop()
	if stopping.complete() || stopping.state.Load() != componentStopRequested {
		t.Fatalf("stopping state = %d", stopping.state.Load())
	}
}

func TestRunPreCanceledStartsNothingButCleans(t *testing.T) {
	options := validOptions()
	var serverCalls, workerCalls, cleanupCalls atomic.Int32
	options.Server = &testServer{serve: func(net.Listener) error { serverCalls.Add(1); return nil }, shutdown: func(context.Context) error { serverCalls.Add(1); return nil }}
	options.Workers[0].Run = func(context.Context) error { workerCalls.Add(1); return nil }
	options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error { cleanupCalls.Add(1); return nil }}}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := daemon.Run(ctx)
	if outcome.ExitCode != 0 || serverCalls.Load() != 0 || workerCalls.Load() != 0 || cleanupCalls.Load() != 1 {
		t.Fatalf("outcome=%+v calls=%d/%d/%d", outcome, serverCalls.Load(), workerCalls.Load(), cleanupCalls.Load())
	}
}

func TestRunNilContextConsumesLifecycleAndCleans(t *testing.T) {
	listener := newTestListener()
	var cleanupCalls atomic.Int32
	options := validOptions()
	options.Listener = listener
	options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error {
		cleanupCalls.Add(1)
		return nil
	}}}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	outcome := daemon.Run(nil)
	if outcome.ExitCode != 1 || outcome.Cause != CauseInvalid || listener.closed.Load() != 1 || cleanupCalls.Load() != 1 {
		t.Fatalf("outcome=%+v closes=%d cleanups=%d", outcome, listener.closed.Load(), cleanupCalls.Load())
	}
	if reused := daemon.Run(context.Background()); reused.ExitCode != 1 || reused.Cause != CauseInvalid {
		t.Fatalf("reused = %+v", reused)
	}
}

func TestRunTypedNilContextConsumesLifecycleAndCleans(t *testing.T) {
	listener := newTestListener()
	var cleanupCalls atomic.Int32
	options := validOptions()
	options.Listener = listener
	options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error {
		cleanupCalls.Add(1)
		return nil
	}}}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	var operator *nilOperatorContext
	outcome := daemon.Run(operator)
	if outcome.ExitCode != 1 || outcome.Cause != CauseInvalid || listener.closed.Load() != 1 || cleanupCalls.Load() != 1 {
		t.Fatalf("outcome=%+v closes=%d cleanups=%d", outcome, listener.closed.Load(), cleanupCalls.Load())
	}
}

func TestRunStartsEveryWorkerBeforeTerminalArbitration(t *testing.T) {
	options := validOptions()
	secondStarted := make(chan struct{})
	options.Workers = []Worker{
		{Name: "first", Run: func(context.Context) error { return errors.New("early failure") }},
		{Name: "second", Run: func(ctx context.Context) error {
			close(secondStarted)
			<-ctx.Done()
			return nil
		}},
	}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	outcome := daemon.Run(context.Background())
	waitSignal(t, secondStarted)
	if outcome.ExitCode != 1 || outcome.Cause != CauseWorker || outcome.Component != "first" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestRunClassifiesEarlyReturnsAndPanics(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Options)
		cause     Cause
		component string
		panicked  bool
	}{
		{name: "worker nil", configure: func(options *Options) { options.Workers[0].Run = func(context.Context) error { return nil } }, cause: CauseWorker, component: "reconciler"},
		{name: "worker error", configure: func(options *Options) {
			options.Workers[0].Run = func(context.Context) error { return errors.New("secret-worker") }
		}, cause: CauseWorker, component: "reconciler"},
		{name: "worker panic", configure: func(options *Options) { options.Workers[0].Run = func(context.Context) error { panic("secret-panic") } }, cause: CauseWorker, component: "reconciler", panicked: true},
		{name: "server nil", configure: func(options *Options) {
			options.Server = &testServer{serve: func(net.Listener) error { return nil }, shutdown: func(context.Context) error { return nil }}
		}, cause: CauseServer, component: "http"},
		{name: "server error", configure: func(options *Options) {
			options.Server = &testServer{serve: func(net.Listener) error { return errors.New("secret-server") }, shutdown: func(context.Context) error { return nil }}
		}, cause: CauseServer, component: "http"},
		{name: "server panic", configure: func(options *Options) {
			options.Server = &testServer{serve: func(net.Listener) error { panic("secret-server-panic") }, shutdown: func(context.Context) error { return nil }}
		}, cause: CauseServer, component: "http", panicked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			options := validOptions()
			options.Logger = testLogger(&logs)
			test.configure(&options)
			daemon, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			outcome := daemon.Run(context.Background())
			if outcome.ExitCode != 1 || outcome.Cause != test.cause || outcome.Component != test.component || outcome.Panicked != test.panicked {
				t.Fatalf("outcome = %+v", outcome)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatalf("secret leaked in logs: %s", logs.String())
			}
		})
	}
}

func TestFailedOutcomeLogsAtErrorLevel(t *testing.T) {
	var logs bytes.Buffer
	options := validOptions()
	options.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))
	options.Workers[0].Run = func(context.Context) error { return errors.New("not logged") }
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	outcome := daemon.Run(context.Background())
	if outcome.ExitCode != 1 || strings.Contains(logs.String(), "daemon started") || !strings.Contains(logs.String(), "daemon stopped") || !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "shutdown_failed=true") {
		t.Fatalf("outcome=%+v logs=%q", outcome, logs.String())
	}
}

func TestStartupAndTerminalLoggingRespectLifecycle(t *testing.T) {
	handler := &blockingTerminalHandler{
		startupLogged:   make(chan struct{}),
		terminalEntered: make(chan struct{}),
		releaseTerminal: make(chan struct{}),
		terminalDone:    make(chan struct{}),
	}
	options := validOptions()
	options.Logger = slog.New(handler)
	options.ShutdownTimeout = 10 * time.Millisecond
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan Outcome, 1)
	go func() { outcomes <- daemon.Run(ctx) }()
	waitSignal(t, handler.startupLogged)
	cancel()
	waitSignal(t, handler.terminalEntered)
	outcome := receiveOutcome(t, outcomes)
	close(handler.releaseTerminal)
	waitSignal(t, handler.terminalDone)
	if outcome.ExitCode != 0 || outcome.DeadlineExpired || outcome.ShutdownFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestLifecycleLoggingFailuresRemainBounded(t *testing.T) {
	t.Run("operator cancellation interrupts startup logging", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})
		handler := &callbackHandler{handle: func(call int32) {
			if call != 1 {
				return
			}
			close(entered)
			<-release
			close(done)
		}}
		options := validOptions()
		options.Logger = slog.New(handler)
		options.ShutdownTimeout = time.Second
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, entered)
		cancel()
		select {
		case outcome := <-outcomes:
			if outcome.ExitCode != 0 || outcome.Cause != CauseOperator || outcome.DeadlineExpired || outcome.ShutdownFailed {
				t.Fatalf("outcome = %+v", outcome)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatal("operator cancellation did not interrupt startup logging")
		}
		close(release)
		waitSignal(t, done)
	})

	t.Run("startup timeout", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})
		handler := &callbackHandler{handle: func(call int32) {
			if call != 1 {
				return
			}
			close(entered)
			<-release
			close(done)
		}}
		options := validOptions()
		options.Logger = slog.New(handler)
		options.ShutdownTimeout = 5 * time.Millisecond
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(context.Background()) }()
		waitSignal(t, entered)
		outcome := receiveOutcome(t, outcomes)
		close(release)
		waitSignal(t, done)
		if outcome.ExitCode != 1 || outcome.Cause != CauseInvalid || !outcome.DeadlineExpired || !outcome.ShutdownFailed {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("startup panic", func(t *testing.T) {
		handler := &callbackHandler{handle: func(call int32) {
			if call == 1 {
				panic("startup log panic")
			}
		}}
		options := validOptions()
		options.Logger = slog.New(handler)
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		outcome := daemon.Run(context.Background())
		if outcome.ExitCode != 1 || outcome.Cause != CauseInvalid || !outcome.Panicked || !outcome.ShutdownFailed {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("terminal panic", func(t *testing.T) {
		startupLogged := make(chan struct{})
		handler := &callbackHandler{handle: func(call int32) {
			if call == 1 {
				close(startupLogged)
				return
			}
			panic("terminal log panic")
		}}
		options := validOptions()
		options.Logger = slog.New(handler)
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, startupLogged)
		cancel()
		outcome := receiveOutcome(t, outcomes)
		if outcome.ExitCode != 0 || outcome.Panicked || outcome.ShutdownFailed {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
}

func TestListenerCloseFailuresAndExpiredHelpers(t *testing.T) {
	tests := []struct {
		name     string
		close    func() error
		panicked bool
	}{
		{name: "close error", close: func() error { return errors.New("close failed") }},
		{name: "close panic", close: func() error { panic("close panic") }, panicked: true},
	}

	t.Run("error classification panic", func(t *testing.T) {
		options := validOptions()
		options.Listener = &callbackListener{close: func() error {
			return &matchError{match: func(error) bool { panic("classification panic") }}
		}}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome := daemon.Run(ctx)
		if outcome.ExitCode != 1 || !outcome.Panicked || !outcome.ShutdownFailed {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("error classification timeout", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})
		options := validOptions()
		options.ShutdownTimeout = 10 * time.Millisecond
		options.Listener = &callbackListener{close: func() error {
			return &matchError{match: func(error) bool {
				close(entered)
				<-release
				close(done)
				return false
			}}
		}}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome := daemon.Run(ctx)
		waitSignal(t, entered)
		close(release)
		waitSignal(t, done)
		if outcome.ExitCode != 1 || !outcome.DeadlineExpired || !outcome.ShutdownFailed {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			options.Listener = &callbackListener{close: test.close}
			daemon, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			outcome := daemon.Run(ctx)
			if outcome.ExitCode != 1 || !outcome.ShutdownFailed || outcome.Panicked != test.panicked {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if completed, _ := boundedAction(ctx, func() { t.Fatal("expired action ran") }); completed {
		t.Fatal("expired action completed")
	}
	if _, completed, _ := runCleanup(ctx, func(context.Context) error { t.Fatal("expired cleanup ran"); return nil }); completed {
		t.Fatal("expired cleanup completed")
	}
}

func TestShutdownFailuresAndPanicsRemainBounded(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
		check  func(Outcome) bool
	}{
		{name: "readiness panic", mutate: func(options *Options) { options.Readiness = &testReadiness{drain: func() { panic("secret") }} }, check: func(outcome Outcome) bool { return outcome.Panicked && outcome.ShutdownFailed }},
		{name: "shutdown error", mutate: func(options *Options) {
			original := options.Server.(*testServer)
			options.Server = &testServer{serve: original.serve, shutdown: func(context.Context) error { _ = options.Listener.Close(); return errors.New("secret") }}
		}, check: func(outcome Outcome) bool { return outcome.ShutdownFailed }},
		{name: "shutdown panic", mutate: func(options *Options) {
			original := options.Server.(*testServer)
			options.Server = &testServer{serve: original.serve, shutdown: func(context.Context) error { _ = options.Listener.Close(); panic("secret") }}
		}, check: func(outcome Outcome) bool { return outcome.ShutdownFailed && outcome.Panicked }},
		{name: "cleanup error", mutate: func(options *Options) {
			options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error { return errors.New("secret") }}}
		}, check: func(outcome Outcome) bool { return outcome.CleanupFailed }},
		{name: "cleanup panic", mutate: func(options *Options) {
			options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error { panic("secret") }}}
		}, check: func(outcome Outcome) bool { return outcome.CleanupFailed && outcome.Panicked }},
		{name: "shutdown timeout", mutate: func(options *Options) {
			options.ShutdownTimeout = 10 * time.Millisecond
			options.Server = &testServer{serve: func(listener net.Listener) error { _, err := listener.Accept(); return err }, shutdown: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
		}, check: func(outcome Outcome) bool { return outcome.DeadlineExpired && outcome.ShutdownFailed }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			test.mutate(&options)
			serverStarted := make(chan struct{})
			originalServer := options.Server.(*testServer)
			options.Server = &testServer{
				serve: func(listener net.Listener) error {
					close(serverStarted)
					return originalServer.serve(listener)
				},
				shutdown: originalServer.shutdown,
			}
			daemon, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			outcomes := make(chan Outcome, 1)
			go func() { outcomes <- daemon.Run(ctx) }()
			waitSignal(t, serverStarted)
			cancel()
			outcome := receiveOutcome(t, outcomes)
			if !test.check(outcome) || outcome.ExitCode != 1 {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestCleanupContinuesInReverseAfterFailure(t *testing.T) {
	options := validOptions()
	started := make(chan struct{})
	originalWorker := options.Workers[0]
	options.Workers[0].Run = func(ctx context.Context) error {
		close(started)
		return originalWorker.Run(ctx)
	}
	var order []string
	options.Cleanups = []Cleanup{
		{Name: "first", Run: func(context.Context) error { order = append(order, "first"); return nil }},
		{Name: "second", Run: func(context.Context) error { order = append(order, "second"); panic("ignored") }},
		{Name: "third", Run: func(context.Context) error { order = append(order, "third"); return errors.New("ignored") }},
	}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan Outcome, 1)
	go func() { outcomes <- daemon.Run(ctx) }()
	waitSignal(t, started)
	cancel()
	outcome := receiveOutcome(t, outcomes)
	if strings.Join(order, ",") != "third,second,first" || !outcome.CleanupFailed {
		t.Fatalf("order=%v outcome=%+v", order, outcome)
	}
}

func TestGlobalDeadlineBoundsWorkerAndCleanup(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Options, chan struct{}, chan struct{})
	}{
		{name: "worker ignores cancellation", configure: func(options *Options, entered, release chan struct{}) {
			options.Workers[0].Run = func(context.Context) error { close(entered); <-release; return nil }
		}},
		{name: "readiness ignores cancellation", configure: func(options *Options, entered, release chan struct{}) {
			options.Readiness = &testReadiness{drain: func() { close(entered); <-release }}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			options.ShutdownTimeout = 10 * time.Millisecond
			entered := make(chan struct{})
			release := make(chan struct{})
			test.configure(&options, entered, release)
			started := make(chan struct{})
			server := options.Server.(*testServer)
			options.Server = &testServer{serve: func(listener net.Listener) error { close(started); return server.serve(listener) }, shutdown: server.shutdown}
			daemon, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			outcomes := make(chan Outcome, 1)
			go func() { outcomes <- daemon.Run(ctx) }()
			waitSignal(t, started)
			cancel()
			waitSignal(t, entered)
			outcome := receiveOutcome(t, outcomes)
			close(release)
			if !outcome.DeadlineExpired || outcome.ExitCode != 1 {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestCleanupCancellationIsJoined(t *testing.T) {
	options := validOptions()
	options.ShutdownTimeout = 10 * time.Millisecond
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	options.Cleanups = []Cleanup{{Name: "blocked", Run: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		close(returned)
		return ctx.Err()
	}}}
	started := make(chan struct{})
	server := options.Server.(*testServer)
	options.Server = &testServer{serve: func(listener net.Listener) error { close(started); return server.serve(listener) }, shutdown: server.shutdown}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan Outcome, 1)
	go func() { outcomes <- daemon.Run(ctx) }()
	waitSignal(t, started)
	cancel()
	waitSignal(t, entered)
	waitSignal(t, canceled)
	select {
	case outcome := <-outcomes:
		t.Fatalf("daemon returned before cleanup joined: %+v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitSignal(t, returned)
	outcome := receiveOutcome(t, outcomes)
	if !outcome.DeadlineExpired || !outcome.CleanupFailed || outcome.ExitCode != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestListenerFallbackIsAttemptedAfterDeadline(t *testing.T) {
	listener := newTestListener()
	readinessEntered := make(chan struct{})
	releaseReadiness := make(chan struct{})
	serverStarted := make(chan struct{})
	options := validOptions()
	options.Listener = listener
	options.ShutdownTimeout = 10 * time.Millisecond
	options.Readiness = &testReadiness{drain: func() {
		close(readinessEntered)
		<-releaseReadiness
	}}
	options.Server = &testServer{
		serve: func(bound net.Listener) error {
			close(serverStarted)
			_, err := bound.Accept()
			return err
		},
		shutdown: func(context.Context) error { return nil },
	}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan Outcome, 1)
	go func() { outcomes <- daemon.Run(ctx) }()
	waitSignal(t, serverStarted)
	cancel()
	waitSignal(t, readinessEntered)
	outcome := receiveOutcome(t, outcomes)
	close(releaseReadiness)
	if listener.closed.Load() != 1 || !outcome.DeadlineExpired || !outcome.ShutdownFailed {
		t.Fatalf("outcome=%+v listener closes=%d", outcome, listener.closed.Load())
	}
}

func TestCleanupDeadlineDoesNotStartEarlierEntries(t *testing.T) {
	options := validOptions()
	options.ShutdownTimeout = 100 * time.Millisecond
	blocked := make(chan struct{})
	done := make(chan struct{})
	var earlier atomic.Int32
	options.Cleanups = []Cleanup{
		{Name: "earlier", Run: func(context.Context) error { earlier.Add(1); return nil }},
		{Name: "blocked", Run: func(ctx context.Context) error { close(blocked); <-ctx.Done(); close(done); return ctx.Err() }},
	}
	started := make(chan struct{})
	server := options.Server.(*testServer)
	options.Server = &testServer{serve: func(listener net.Listener) error { close(started); return server.serve(listener) }, shutdown: server.shutdown}
	daemon, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan Outcome, 1)
	go func() { outcomes <- daemon.Run(ctx) }()
	waitSignal(t, started)
	cancel()
	waitSignal(t, blocked)
	outcome := receiveOutcome(t, outcomes)
	waitSignal(t, done)
	if !outcome.DeadlineExpired || earlier.Load() != 0 {
		t.Fatalf("outcome=%+v earlier=%d", outcome, earlier.Load())
	}
}

func TestDependenciesRemainOwnedUntilServerAndWorkersStop(t *testing.T) {
	t.Run("listener unblocks server before cleanup", func(t *testing.T) {
		listener := newTestListener()
		serverStarted := make(chan struct{})
		serverExited := make(chan struct{})
		var cleanupCalls atomic.Int32
		options := validOptions()
		options.Listener = listener
		options.Server = &testServer{
			serve: func(bound net.Listener) error {
				close(serverStarted)
				_, err := bound.Accept()
				close(serverExited)
				return err
			},
			shutdown: func(context.Context) error { return nil },
		}
		options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error {
			select {
			case <-serverExited:
				cleanupCalls.Add(1)
				return nil
			default:
				return errors.New("server still active")
			}
		}}}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, serverStarted)
		cancel()
		outcome := receiveOutcome(t, outcomes)
		if cleanupCalls.Load() != 1 || outcome.CleanupFailed {
			t.Fatalf("outcome=%+v cleanup calls=%d", outcome, cleanupCalls.Load())
		}
	})

	for _, test := range []struct {
		name            string
		shutdown        func(context.Context) error
		wantPanicked    bool
		wantDeadline    bool
		shutdownTimeout time.Duration
	}{
		{name: "shutdown error", shutdown: func(context.Context) error { return errors.New("drain failed") }},
		{name: "shutdown panic", shutdown: func(context.Context) error { panic("drain panic") }, wantPanicked: true},
		{name: "shutdown deadline", shutdown: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, wantDeadline: true, shutdownTimeout: 10 * time.Millisecond},
	} {
		t.Run(test.name+" retains dependencies", func(t *testing.T) {
			listener := newTestListener()
			serverStarted := make(chan struct{})
			var cleanupCalls atomic.Int32
			options := validOptions()
			options.Listener = listener
			if test.shutdownTimeout > 0 {
				options.ShutdownTimeout = test.shutdownTimeout
			}
			options.Server = &testServer{
				serve: func(bound net.Listener) error {
					close(serverStarted)
					_, err := bound.Accept()
					return err
				},
				shutdown: test.shutdown,
			}
			options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error {
				cleanupCalls.Add(1)
				return nil
			}}}
			daemon, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			outcomes := make(chan Outcome, 1)
			go func() { outcomes <- daemon.Run(ctx) }()
			waitSignal(t, serverStarted)
			cancel()
			outcome := receiveOutcome(t, outcomes)
			if cleanupCalls.Load() != 0 || !outcome.ShutdownFailed || !outcome.CleanupFailed || outcome.Panicked != test.wantPanicked || outcome.DeadlineExpired != test.wantDeadline {
				t.Fatalf("outcome=%+v cleanup calls=%d", outcome, cleanupCalls.Load())
			}
		})
	}

	t.Run("active worker prevents cleanup", func(t *testing.T) {
		workerStarted := make(chan struct{})
		releaseWorker := make(chan struct{})
		var cleanupCalls atomic.Int32
		options := validOptions()
		options.ShutdownTimeout = 10 * time.Millisecond
		options.Workers[0].Run = func(context.Context) error {
			close(workerStarted)
			<-releaseWorker
			return nil
		}
		options.Cleanups = []Cleanup{{Name: "store", Run: func(context.Context) error {
			cleanupCalls.Add(1)
			return nil
		}}}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, workerStarted)
		cancel()
		outcome := receiveOutcome(t, outcomes)
		close(releaseWorker)
		if cleanupCalls.Load() != 0 || !outcome.DeadlineExpired || !outcome.CleanupFailed {
			t.Fatalf("outcome=%+v cleanup calls=%d", outcome, cleanupCalls.Load())
		}
	})
}

func TestListenerCloseAndWorkerTeardownFailuresAreNotClean(t *testing.T) {
	t.Run("blocking listener close", func(t *testing.T) {
		listener := &blockingListener{entered: make(chan struct{}), release: make(chan struct{})}
		options := validOptions()
		options.Listener = listener
		options.ShutdownTimeout = 10 * time.Millisecond
		serverStarted := make(chan struct{})
		options.Server = &testServer{
			serve:    func(net.Listener) error { close(serverStarted); <-listener.release; return nil },
			shutdown: func(context.Context) error { return nil },
		}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, serverStarted)
		cancel()
		waitSignal(t, listener.entered)
		outcome := receiveOutcome(t, outcomes)
		close(listener.release)
		if !outcome.DeadlineExpired || !outcome.ShutdownFailed || outcome.ExitCode != 1 {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("worker teardown error", func(t *testing.T) {
		options := validOptions()
		workerStarted := make(chan struct{})
		options.Workers[0].Run = func(ctx context.Context) error {
			close(workerStarted)
			<-ctx.Done()
			return errors.New("secret teardown")
		}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, workerStarted)
		cancel()
		outcome := receiveOutcome(t, outcomes)
		if !outcome.ShutdownFailed || outcome.ExitCode != 1 {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("worker teardown deadline exceeded", func(t *testing.T) {
		options := validOptions()
		workerStarted := make(chan struct{})
		options.Workers[0].Run = func(ctx context.Context) error {
			close(workerStarted)
			<-ctx.Done()
			return context.DeadlineExceeded
		}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(ctx) }()
		waitSignal(t, workerStarted)
		cancel()
		outcome := receiveOutcome(t, outcomes)
		if !outcome.ShutdownFailed || outcome.ExitCode != 1 {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
}

func TestConcurrentTerminalTriggersProduceOneSafeOutcome(t *testing.T) {
	for iteration := 0; iteration < 25; iteration++ {
		options := validOptions()
		release := make(chan struct{})
		entered := make(chan struct{}, 2)
		options.Workers = []Worker{
			{Name: "first", Run: func(context.Context) error { entered <- struct{}{}; <-release; return nil }},
			{Name: "second", Run: func(context.Context) error { entered <- struct{}{}; <-release; return errors.New("not logged") }},
		}
		daemon, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		outcomes := make(chan Outcome, 1)
		go func() { outcomes <- daemon.Run(context.Background()) }()
		waitSignal(t, entered)
		waitSignal(t, entered)
		close(release)
		outcome := receiveOutcome(t, outcomes)
		if outcome.ExitCode != 1 || outcome.Cause != CauseWorker || (outcome.Component != "first" && outcome.Component != "second") {
			t.Fatalf("outcome = %+v", outcome)
		}
	}
}

func receiveOutcome(t *testing.T, outcomes <-chan Outcome) Outcome {
	t.Helper()
	select {
	case outcome := <-outcomes:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop")
		return Outcome{}
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lifecycle barrier")
	}
}

func wantOrder(t *testing.T, events []string, wanted ...string) {
	t.Helper()
	position := -1
	for _, expected := range wanted {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == expected {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("%q missing after position %d in %v", expected, position, events)
		}
		position = found
	}
}
