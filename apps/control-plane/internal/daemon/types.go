package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"
)

const maxComponentNameLength = 48

var (
	// ErrInvalidOptions reports an invalid lifecycle definition.
	ErrInvalidOptions = errors.New("invalid daemon options")
	// ErrReused reports an attempt to run a one-shot daemon more than once.
	ErrReused = errors.New("daemon already run")
)

// Server is the long-running HTTP serving boundary supervised by Daemon.
type Server interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
}

// Readiness moves the process into its terminal draining state.
type Readiness interface {
	SetDraining()
}

// Worker describes one named critical background component.
type Worker struct {
	Name string
	Run  func(context.Context) error
}

// Cleanup describes one named resource release operation. Run must honor
// context cancellation and return before its deadline so shutdown can join it.
type Cleanup struct {
	Name string
	Run  func(context.Context) error
}

// Options defines a prepared daemon and all resources transferred to it.
type Options struct {
	Logger          *slog.Logger
	Listener        net.Listener
	Server          Server
	Readiness       Readiness
	Workers         []Worker
	Cleanups        []Cleanup
	ShutdownTimeout time.Duration
}

// Cause is the stable initiating reason for lifecycle termination.
type Cause string

const (
	CauseOperator Cause = "operator_termination"
	CauseServer   Cause = "server_termination"
	CauseWorker   Cause = "worker_termination"
	CauseInvalid  Cause = "invalid_lifecycle"
)

// Outcome is the safe, structured result of one daemon run.
type Outcome struct {
	ExitCode        int
	Cause           Cause
	Component       string
	DeadlineExpired bool
	ShutdownFailed  bool
	CleanupFailed   bool
	Panicked        bool
}
