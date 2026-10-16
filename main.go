package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ekovshilovsky/op-forward/cmd"
)

func main() {
	os.Exit(run(os.Stderr))
}

// run executes the command line in os.Args and returns the process exit code.
//
// A cmd.ExitStatus carries an exit code the command has already explained on
// stderr, so it is returned without printing anything further. This is what
// lets the proxy hand the shim either the forwarded op exit code or the 127
// infrastructure-failure code unchanged. Any other error is printed to stderr
// and reported as exit code 1.
func run(stderr io.Writer) int {
	err := cmd.Execute()
	if err == nil {
		return 0
	}

	var status cmd.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}

	// A failure to write the diagnostic cannot itself be reported anywhere.
	_, _ = fmt.Fprintln(stderr, err)
	return 1
}
