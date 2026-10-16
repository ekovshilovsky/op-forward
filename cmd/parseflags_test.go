package cmd

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseFlags must reproduce what flag.ExitOnError did: a help request ends the
// process successfully, any other parse failure ends it with status 2, and in
// both cases the flag package's diagnostics and usage text go to stderr.
func TestParseFlagsMapsOutcomesToExitStatus(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStatus int
		wantErr    bool
		wantStderr []string
	}{
		{name: "valid flag", args: []string{"--port", "1234"}},
		{name: "no arguments", args: nil},
		{name: "short help", args: []string{"-h"}, wantErr: true, wantStatus: 0, wantStderr: []string{"Usage of t:", "-port int"}},
		{name: "long help", args: []string{"-help"}, wantErr: true, wantStatus: 0, wantStderr: []string{"Usage of t:"}},
		{name: "double dash help", args: []string{"--help"}, wantErr: true, wantStatus: 0, wantStderr: []string{"Usage of t:"}},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true, wantStatus: 2, wantStderr: []string{"flag provided but not defined: -bogus", "Usage of t:"}},
		{name: "invalid value", args: []string{"--port", "abc"}, wantErr: true, wantStatus: 2, wantStderr: []string{`invalid value "abc" for flag -port`, "Usage of t:"}},
		{name: "missing value", args: []string{"--port"}, wantErr: true, wantStatus: 2, wantStderr: []string{"flag needs an argument: -port", "Usage of t:"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.Int("port", 1, "Port to use")
			read := captureStdio(t)

			err := parseFlags(fs, tc.args)
			stdout, stderr := read()

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("parseFlags(%v) = %v, want nil", tc.args, err)
				}
				if stdout != "" || stderr != "" {
					t.Errorf("a successful parse wrote stdout=%q stderr=%q", stdout, stderr)
				}
				return
			}

			requireExitStatus(t, err, tc.wantStatus)
			if stdout != "" {
				t.Errorf("stdout = %q, want empty (diagnostics belong on stderr)", stdout)
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr is missing %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// The status returned for a help request is an error value, not nil, so the
// process entry point can tell "stop successfully" from "keep going".
func TestParseFlagsHelpIsNonNilZeroStatus(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	read := captureStdio(t)

	err := parseFlags(fs, []string{"-h"})
	_, _ = read()

	var status ExitStatus
	if !errors.As(err, &status) || status != 0 {
		t.Fatalf("parseFlags(-h) = %v (%T), want a non-nil ExitStatus(0)", err, err)
	}
}

// Every command that parses flags reports help and usage errors the same way
// through Execute. Help and bad flags are rejected before the command touches
// tokens, the network, or the shim directory, which the empty HOME confirms.
func TestExecuteFlagParsingStatusForEachFlagCommand(t *testing.T) {
	for _, command := range []string{"proxy", "serve", "install"} {
		for _, tc := range []struct {
			name       string
			flagArg    string
			wantStatus int
			wantStderr string
		}{
			{"help", "-h", 0, "Usage of " + command + ":"},
			{"unknown flag", "--bogus", 2, "flag provided but not defined: -bogus"},
		} {
			t.Run(command+" "+tc.name, func(t *testing.T) {
				home := tempHome(t)
				t.Setenv("OP_FORWARD_TOKEN_DIR", filepath.Join(home, "tokens"))
				t.Setenv("OP_FORWARD_TOKEN_FILE", "")
				setArgs(t, "op-forward", command, tc.flagArg)
				read := captureStdio(t)

				err := Execute()
				stdout, stderr := read()

				requireExitStatus(t, err, tc.wantStatus)
				if stdout != "" {
					t.Errorf("stdout = %q, want empty", stdout)
				}
				if !strings.Contains(stderr, tc.wantStderr) {
					t.Errorf("stderr is missing %q:\n%s", tc.wantStderr, stderr)
				}
				entries, readErr := os.ReadDir(home)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(entries) != 0 {
					t.Errorf("the command created files under HOME before parsing flags: %v", entries)
				}
			})
		}
	}
}
