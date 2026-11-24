package main

import (
	"fmt"
	"github.com/mevlutkural/moduleos/deploy/swarm/operations"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Println("Operational evidence unavailable")
		os.Exit(1)
	}
	defer f.Close()
	if operations.Summary(f, os.Stdout) != nil {
		fmt.Println("Invalid operational evidence")
		os.Exit(1)
	}
}
