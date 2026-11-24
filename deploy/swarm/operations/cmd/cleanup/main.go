package main

import (
	"context"
	"encoding/json"
	"github.com/mevlutkural/moduleos/deploy/swarm/operations"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 3 || os.Getenv("MODULEOS_STACK_OPERATIONS") != "1" || os.Getenv("MODULEOS_STACK_DISPOSABLE") != "1" {
		os.Exit(2)
	}
	l, err := operations.ReadLedger(os.Args[1], os.Args[2])
	if err != nil {
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		os.Exit(2)
	}
	guard, err := os.ReadFile(os.Getenv("MODULEOS_OPERATIONS_GUARD"))
	if err != nil {
		os.Exit(2)
	}
	info, err := operations.Docker(ctx, 5*time.Second, "info", "--format", "{{json .}}")
	var engine struct {
		ID, OperatingSystem, Architecture string
		Swarm                             struct{ ControlAvailable bool }
	}
	if err != nil || json.Unmarshal(info, &engine) != nil || string(guard) != engine.ID+" "+os.Args[2] || engine.Architecture != "x86_64" || !engine.Swarm.ControlAvailable || strings.Contains(engine.OperatingSystem, "OrbStack") || strings.Contains(engine.OperatingSystem, "Docker Desktop") {
		os.Exit(2)
	}
	if operations.CleanupLedger(ctx, l) != nil {
		os.Exit(1)
	}
}
