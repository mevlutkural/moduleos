package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type lifecycleState uint8

const (
	statePrepared lifecycleState = iota
	stateRunning
	stateStopping
	stateStopped
)

type componentResult struct {
	kind       Cause
	component  string
	err        error
	panicked   bool
	unexpected bool
}

type terminalArbiter struct {
	mu           sync.Mutex
	terminated   bool
	startupBegun bool
	first        chan componentResult
	done         chan struct{}
}

const (
	componentActive uint32 = iota
	componentCompleted
	componentStopRequested
)

type completionGate struct{ state atomic.Uint32 }

func (gate *completionGate) complete() bool {
	return gate.state.CompareAndSwap(componentActive, componentCompleted)
}

func (gate *completionGate) requestStop() {
	gate.state.CompareAndSwap(componentActive, componentStopRequested)
}

func newTerminalArbiter() *terminalArbiter {
	return &terminalArbiter{first: make(chan componentResult, 1), done: make(chan struct{})}
}

func (arbiter *terminalArbiter) report(result componentResult) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if arbiter.terminated {
		return
	}
	arbiter.terminated = true
	arbiter.first <- result
	close(arbiter.done)
}

func (arbiter *terminalArbiter) beginStartup(start func()) bool {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if arbiter.terminated || arbiter.startupBegun {
		return false
	}
	arbiter.startupBegun = true
	start()
	return true
}

func (arbiter *terminalArbiter) poll() *componentResult {
	select {
	case result := <-arbiter.first:
		return &result
	default:
		return nil
	}
}

type Daemon struct {
	logger          *slog.Logger
	listener        *onceListener
	server          Server
	readiness       Readiness
	workers         []Worker
	cleanups        []Cleanup
	shutdownTimeout time.Duration

	mu    sync.Mutex
	state lifecycleState
}

// New validates a prepared lifecycle without starting any component.
func New(options Options) (*Daemon, error) {
	if options.Logger == nil || isNil(options.Listener) || isNil(options.Server) || isNil(options.Readiness) {
		return nil, fmt.Errorf("%w: required collaborator is nil", ErrInvalidOptions)
	}
	if options.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("%w: shutdown timeout must be positive", ErrInvalidOptions)
	}
	if len(options.Workers) == 0 {
		return nil, fmt.Errorf("%w: at least one worker is required", ErrInvalidOptions)
	}
	if err := validateWorkers(options.Workers); err != nil {
		return nil, err
	}
	if err := validateCleanups(options.Cleanups); err != nil {
		return nil, err
	}

	return &Daemon{
		logger:          options.Logger,
		listener:        &onceListener{Listener: options.Listener},
		server:          options.Server,
		readiness:       options.Readiness,
		workers:         append([]Worker(nil), options.Workers...),
		cleanups:        append([]Cleanup(nil), options.Cleanups...),
		shutdownTimeout: options.ShutdownTimeout,
		state:           statePrepared,
	}, nil
}

func (d *Daemon) Run(operator context.Context) Outcome {
	if isNil(operator) {
		if !d.beginRun() {
			return Outcome{ExitCode: 1, Cause: CauseInvalid}
		}
		_, cancelWorkers := context.WithCancel(context.Background())
		return d.shutdown(Outcome{ExitCode: 1, Cause: CauseInvalid}, cancelWorkers, make(chan componentResult, 1), nil, nil, false, nil, nil)
	}
	if !d.beginRun() {
		return Outcome{ExitCode: 1, Cause: CauseInvalid}
	}

	results := make(chan componentResult, len(d.workers)+2)
	arbiter := newTerminalArbiter()
	stopOperator := context.AfterFunc(operator, func() { arbiter.report(componentResult{kind: CauseOperator}) })
	defer stopOperator()
	workerContext, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()

	if err := operator.Err(); err != nil {
		arbiter.report(componentResult{kind: CauseOperator})
		return d.shutdown(Outcome{Cause: CauseOperator}, cancelWorkers, results, nil, nil, false, nil, nil)
	}

	var workerGroup sync.WaitGroup
	workerGates := make([]*completionGate, 0, len(d.workers))
	var primary *componentResult
	for _, worker := range d.workers {
		started := make(chan struct{})
		gate := &completionGate{}
		workerGates = append(workerGates, gate)
		workerGroup.Add(1)
		go d.runWorker(workerContext, worker, gate, started, arbiter, results, &workerGroup)
		<-started
	}

	if operator.Err() != nil {
		arbiter.report(componentResult{kind: CauseOperator})
	}
	primary = arbiter.poll()

	var serverGroup sync.WaitGroup
	serverStarted := false
	var serverGate *completionGate
	if primary == nil {
		if !arbiter.beginStartup(func() {
			started := make(chan struct{})
			serverGate = &completionGate{}
			serverGroup.Add(1)
			serverStarted = true
			go d.runServer(serverGate, started, arbiter, results, &serverGroup)
			<-started
		}) {
			primary = arbiter.poll()
		}
	}

	if primary == nil {
		startupContext, cancelStartup := context.WithTimeout(context.Background(), d.shutdownTimeout)
		logged, logPanicked := d.boundedLog(startupContext, arbiter.done, slog.LevelInfo, "daemon started",
			"component", "daemon",
			"phase", "running",
			"worker_count", len(d.workers),
		)
		cancelStartup()
		if !logged || logPanicked {
			if trigger := arbiter.poll(); trigger != nil {
				outcome := Outcome{Cause: trigger.kind, Component: trigger.component, Panicked: trigger.panicked || logPanicked}
				if trigger.kind != CauseOperator || logPanicked {
					outcome.ExitCode = 1
				}
				return d.shutdown(outcome, cancelWorkers, results, &serverGroup, &workerGroup, serverStarted, serverGate, workerGates)
			}
			outcome := Outcome{ExitCode: 1, Cause: CauseInvalid, Component: "logger", Panicked: logPanicked, ShutdownFailed: true}
			if !logged {
				outcome.DeadlineExpired = true
			}
			return d.shutdown(outcome, cancelWorkers, results, &serverGroup, &workerGroup, serverStarted, serverGate, workerGates)
		}
		result := <-arbiter.first
		primary = &result
	}

	outcome := Outcome{Cause: primary.kind, Component: primary.component, Panicked: primary.panicked}
	if primary.kind != CauseOperator {
		outcome.ExitCode = 1
	}
	return d.shutdown(outcome, cancelWorkers, results, &serverGroup, &workerGroup, serverStarted, serverGate, workerGates)
}

func (d *Daemon) beginRun() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != statePrepared {
		return false
	}
	d.state = stateRunning
	return true
}

func (d *Daemon) runWorker(ctx context.Context, worker Worker, gate *completionGate, started chan<- struct{}, arbiter *terminalArbiter, results chan<- componentResult, group *sync.WaitGroup) {
	defer group.Done()
	close(started)
	result := componentResult{kind: CauseWorker, component: worker.Name}
	func() {
		defer func() {
			if recover() != nil {
				result.panicked = true
			}
		}()
		result.err = worker.Run(ctx)
		result.unexpected = gate.complete()
	}()
	arbiter.report(result)
	results <- result
}

func (d *Daemon) runServer(gate *completionGate, started chan<- struct{}, arbiter *terminalArbiter, results chan<- componentResult, group *sync.WaitGroup) {
	defer group.Done()
	close(started)
	result := componentResult{kind: CauseServer, component: "http"}
	func() {
		defer func() {
			if recover() != nil {
				result.panicked = true
			}
		}()
		result.err = d.server.Serve(d.listener)
		result.unexpected = gate.complete()
	}()
	arbiter.report(result)
	results <- result
}

func (d *Daemon) shutdown(outcome Outcome, cancelWorkers context.CancelFunc, results <-chan componentResult, serverGroup, workerGroup *sync.WaitGroup, serverStarted bool, serverGate *completionGate, workerGates []*completionGate) Outcome {
	d.mu.Lock()
	d.state = stateStopping
	d.mu.Unlock()

	shutdownContext, cancel := context.WithTimeout(context.Background(), d.shutdownTimeout)
	defer cancel()

	serverDrained := !serverStarted
	serverStopped := !serverStarted
	if serverStarted {
		if completed, didPanic := boundedAction(shutdownContext, d.readiness.SetDraining); !completed {
			outcome.DeadlineExpired = true
			outcome.ShutdownFailed = true
		} else if didPanic {
			outcome.ShutdownFailed = true
			outcome.Panicked = true
		}

		serverGate.requestStop()
		if err, completed, didPanic := bounded(shutdownContext, d.server.Shutdown); !completed {
			outcome.DeadlineExpired = true
			outcome.ShutdownFailed = true
		} else if err != nil || didPanic {
			outcome.ShutdownFailed = true
			outcome.Panicked = outcome.Panicked || didPanic
		} else {
			serverDrained = true
		}

	}

	closeError, closeCompleted, closePanicked := boundedListenerClose(shutdownContext, d.listener.Close)
	if !closeCompleted {
		outcome.DeadlineExpired = true
		outcome.ShutdownFailed = true
	} else if closePanicked {
		outcome.Panicked = true
		outcome.ShutdownFailed = true
	} else if closeError != nil {
		closed, classified, classificationPanicked := boundedErrorMatch(shutdownContext, closeError, net.ErrClosed)
		if !classified {
			outcome.DeadlineExpired = true
			outcome.ShutdownFailed = true
		} else if classificationPanicked {
			outcome.Panicked = true
			outcome.ShutdownFailed = true
		} else if !closed {
			outcome.ShutdownFailed = true
		}
	}

	if serverStarted {
		if serverGroup == nil || waitGroup(shutdownContext, serverGroup) {
			serverStopped = true
		} else {
			outcome.DeadlineExpired = true
			outcome.ShutdownFailed = true
		}
	}
	for _, gate := range workerGates {
		gate.requestStop()
	}
	cancelWorkers()
	workersStopped := workerGroup == nil || waitGroup(shutdownContext, workerGroup)
	if !workersStopped {
		outcome.DeadlineExpired = true
		outcome.ShutdownFailed = true
	}

	if serverDrained && serverStopped && workersStopped {
		for index := len(d.cleanups) - 1; index >= 0; index-- {
			cleanup := d.cleanups[index]
			err, completed, didPanic := runCleanup(shutdownContext, cleanup.Run)
			if !completed {
				outcome.DeadlineExpired = true
				outcome.CleanupFailed = true
				break
			} else if err != nil || didPanic {
				outcome.CleanupFailed = true
				outcome.Panicked = outcome.Panicked || didPanic
			}
		}
	} else if len(d.cleanups) > 0 {
		outcome.CleanupFailed = true
	}
	for {
		select {
		case result := <-results:
			if result.panicked {
				outcome.Panicked = true
			}
			unexpectedError, classified, classificationPanicked := unexpectedShutdownError(shutdownContext, result)
			if !classified {
				outcome.DeadlineExpired = true
				outcome.ShutdownFailed = true
			}
			if classificationPanicked {
				outcome.Panicked = true
				outcome.ShutdownFailed = true
			}
			if result.panicked || result.unexpected || unexpectedError {
				outcome.ShutdownFailed = true
			}
		default:
			goto drained
		}
	}

drained:
	if shutdownContext.Err() != nil {
		outcome.DeadlineExpired = true
	}
	if outcome.Cause != CauseOperator || outcome.DeadlineExpired || outcome.ShutdownFailed || outcome.CleanupFailed || outcome.Panicked {
		outcome.ExitCode = 1
	}

	attributes := []any{
		"component", outcome.Component,
		"phase", "stopped",
		"cause", outcome.Cause,
		"exit_code", outcome.ExitCode,
		"deadline_expired", outcome.DeadlineExpired,
		"shutdown_failed", outcome.ShutdownFailed,
		"cleanup_failed", outcome.CleanupFailed,
		"panicked", outcome.Panicked,
	}
	level := slog.LevelInfo
	if outcome.ExitCode != 0 {
		level = slog.LevelError
	}
	_, _ = d.boundedLog(shutdownContext, nil, level, "daemon stopped", attributes...)
	d.mu.Lock()
	d.state = stateStopped
	d.mu.Unlock()
	return outcome
}

func (d *Daemon) boundedLog(ctx context.Context, abort <-chan struct{}, level slog.Level, message string, attributes ...any) (bool, bool) {
	started := make(chan struct{})
	result := make(chan bool, 1)
	go func() {
		close(started)
		didPanic := false
		func() {
			defer func() { didPanic = recover() != nil }()
			d.logger.Log(ctx, level, message, attributes...)
		}()
		result <- didPanic
	}()
	<-started
	select {
	case didPanic := <-result:
		return true, didPanic
	case <-ctx.Done():
		return false, false
	case <-abort:
		return false, false
	}
}

func unexpectedShutdownError(ctx context.Context, result componentResult) (bool, bool, bool) {
	if result.err == nil {
		return false, true, false
	}
	targets := []error{context.Canceled}
	if result.kind == CauseServer {
		targets = append(targets, net.ErrClosed, http.ErrServerClosed)
	}
	expected, completed, panicked := boundedErrorMatch(ctx, result.err, targets...)
	return !expected, completed, panicked
}

func boundedErrorMatch(ctx context.Context, err error, targets ...error) (bool, bool, bool) {
	if ctx.Err() != nil {
		return false, false, false
	}
	type matchResult struct {
		matched  bool
		panicked bool
	}
	result := make(chan matchResult, 1)
	go func() {
		value := matchResult{}
		defer func() {
			value.panicked = recover() != nil
			result <- value
		}()
		for _, target := range targets {
			if errors.Is(err, target) {
				value.matched = true
				return
			}
		}
	}()
	select {
	case value := <-result:
		return value.matched, true, value.panicked
	case <-ctx.Done():
		return false, false, false
	}
}

func validateWorkers(workers []Worker) error {
	names := make(map[string]struct{}, len(workers))
	for _, worker := range workers {
		if err := validateName(worker.Name); err != nil || worker.Run == nil {
			return fmt.Errorf("%w: invalid worker", ErrInvalidOptions)
		}
		if _, exists := names[worker.Name]; exists {
			return fmt.Errorf("%w: duplicate worker", ErrInvalidOptions)
		}
		names[worker.Name] = struct{}{}
	}
	return nil
}

func validateCleanups(cleanups []Cleanup) error {
	names := make(map[string]struct{}, len(cleanups))
	for _, cleanup := range cleanups {
		if err := validateName(cleanup.Name); err != nil || cleanup.Run == nil {
			return fmt.Errorf("%w: invalid cleanup", ErrInvalidOptions)
		}
		if _, exists := names[cleanup.Name]; exists {
			return fmt.Errorf("%w: duplicate cleanup", ErrInvalidOptions)
		}
		names[cleanup.Name] = struct{}{}
	}
	return nil
}

func validateName(name string) error {
	if name == "" || len(name) > maxComponentNameLength {
		return ErrInvalidOptions
	}
	for _, character := range name {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return ErrInvalidOptions
		}
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && reflect.ValueOf(value).IsNil()
}

func bounded(ctx context.Context, function func(context.Context) error) (error, bool, bool) {
	type callResult struct {
		err      error
		panicked bool
	}
	if ctx.Err() != nil {
		return nil, false, false
	}
	result := make(chan callResult, 1)
	go func() {
		value := callResult{}
		func() {
			defer func() { value.panicked = recover() != nil }()
			value.err = function(ctx)
		}()
		result <- value
	}()
	select {
	case value := <-result:
		return value.err, true, value.panicked
	case <-ctx.Done():
		return nil, false, false
	}
}

func boundedListenerClose(ctx context.Context, function func() error) (error, bool, bool) {
	type callResult struct {
		err      error
		panicked bool
	}
	started := make(chan struct{})
	result := make(chan callResult, 1)
	go func() {
		close(started)
		value := callResult{}
		func() {
			defer func() { value.panicked = recover() != nil }()
			value.err = function()
		}()
		result <- value
	}()
	<-started
	select {
	case value := <-result:
		return value.err, true, value.panicked
	case <-ctx.Done():
		return nil, false, false
	}
}

func boundedAction(ctx context.Context, function func()) (bool, bool) {
	if ctx.Err() != nil {
		return false, false
	}
	result := make(chan bool, 1)
	go func() {
		didPanic := false
		func() {
			defer func() { didPanic = recover() != nil }()
			function()
		}()
		result <- didPanic
	}()
	select {
	case didPanic := <-result:
		return true, didPanic
	case <-ctx.Done():
		return false, false
	}
}

func runCleanup(ctx context.Context, function func(context.Context) error) (err error, completed, didPanic bool) {
	if ctx.Err() != nil {
		return nil, false, false
	}
	completed = true
	defer func() {
		if recover() != nil {
			didPanic = true
		}
	}()
	err = function(ctx)
	return err, completed, false
}

func waitGroup(ctx context.Context, group *sync.WaitGroup) bool {
	if ctx.Err() != nil {
		return false
	}
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

type onceListener struct {
	net.Listener
	once sync.Once
	err  error
}

func (listener *onceListener) Close() error {
	listener.once.Do(func() { listener.err = listener.Listener.Close() })
	return listener.err
}
