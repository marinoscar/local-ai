package main

import (
	"context"
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/cmd/aiagent/setup"
)

// The INST-02 steps live in package setup (cmd/aiagent/setup); each one checks root itself, so the
// read-only ones can run as a normal user (installer.md section 3.5).
var setupAnyUser = map[string]bool{"preflight": true, "profile-suggest": true, "catalog": true}

func init() {
	for name := range setupAnyUser {
		step := name
		setupSteps[step] = func(args []string, stdout, stderr io.Writer) int {
			return setup.Run(context.Background(), append([]string{step}, args...), os.Stdin, stdout, stderr)
		}
	}
}
