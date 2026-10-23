package main

import (
	"os"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/bootstrap"
)

func main() {
	os.Exit(bootstrap.Run())
}
