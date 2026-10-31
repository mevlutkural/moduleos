package main

import (
	"os"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/containerinit"
)

func main() {
	os.Exit(containerinit.Main(os.Args[1:]))
}
