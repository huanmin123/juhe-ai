package main

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/cmdroot"
	"juhe-ai/backend-go/internal/logging"
)

func main() {
	os.Exit(executeCommand(cmdroot.New(os.Stdout, os.Stderr), os.Stderr))
}

func executeCommand(root *cobra.Command, stderr io.Writer) int {
	root.SetErr(stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.Execute(); err != nil {
		logging.WriteFatal(stderr, err)
		return 1
	}
	return 0
}
