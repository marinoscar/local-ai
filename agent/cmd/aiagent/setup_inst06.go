package main

import (
	"context"
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/cmd/aiagent/setup"
)

// The INST-06 steps (node-yaml, units, api-key, installer.md section 3.5) live in cmd/aiagent/setup; they
// join the step table here. runSetup has already checked for root.
func init() {
	for _, name := range []string{"node-yaml", "units", "api-key"} {
		setupSteps[name] = func(args []string, stdout, stderr io.Writer) int {
			return setup.Run(context.Background(), append([]string{name}, args...), os.Stdin, stdout, stderr)
		}
	}
}
