package main

import (
	"os"

	"juhe-ai/backend-go/internal/cmdroot"
)

func main() {
	if err := cmdroot.New(os.Stdout, os.Stderr).Execute(); err != nil {
		os.Exit(1)
	}
}
