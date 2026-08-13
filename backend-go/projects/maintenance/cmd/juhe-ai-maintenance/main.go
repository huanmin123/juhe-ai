package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func main() {
	version := flag.Bool("version", false, "print the maintenance project contract version")
	check := flag.Bool("check-boundary", false, "verify the scaffold boundary")
	flag.Parse()
	if *version {
		fmt.Printf("juhe-ai-maintenance project=%s contract=%s\n", contracts.ProjectMaintenance, contracts.ArchitectureVersion)
		return
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return
	}
	fmt.Fprintln(os.Stderr, "maintenance project runtime is not switched yet; select an explicit one-shot command")
	os.Exit(2)
}
