//go:build !windows

package cmd

import (
	"os"
	"strings"
	"testing"
)

// usageSubcommands lists every subcommand Execute dispatches. The usage text
// is the only in-tool documentation of them, so each must appear there.
var usageSubcommands = []string{
	"op-forward serve",
	"op-forward install",
	"op-forward proxy",
	"op-forward service install",
	"op-forward service uninstall",
	"op-forward update",
	"op-forward doctor",
	"op-forward version",
}

func TestPrintUsageListsEverySubcommand(t *testing.T) {
	withVersion(t, "1.2.3-test")
	out := captureStdio(t)
	printUsage()
	stdout, stderr := out()

	if stderr != "" {
		t.Errorf("usage must be written to stdout only, stderr was %q", stderr)
	}
	if !strings.HasPrefix(stdout, "op-forward 1.2.3-test") {
		t.Errorf("usage should open with the running version, got %q", firstLine(stdout))
	}
	for _, sub := range usageSubcommands {
		if !strings.Contains(stdout, sub) {
			t.Errorf("usage does not mention %q", sub)
		}
	}
}

func TestExecuteWithoutArgumentsPrintsUsage(t *testing.T) {
	setArgs(t, "op-forward")
	out := captureStdio(t)
	err := Execute()
	stdout, _ := out()

	if err != nil {
		t.Fatalf("Execute() with no arguments = %v, want nil", err)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("expected usage text, got %q", stdout)
	}
}

func TestExecuteHelpAliasesPrintUsage(t *testing.T) {
	for _, alias := range []string{"help", "--help", "-h"} {
		t.Run(alias, func(t *testing.T) {
			setArgs(t, "op-forward", alias)
			out := captureStdio(t)
			err := Execute()
			stdout, stderr := out()

			if err != nil {
				t.Fatalf("Execute(%s) = %v, want nil", alias, err)
			}
			if stderr != "" {
				t.Errorf("help must not write to stderr, got %q", stderr)
			}
			for _, sub := range usageSubcommands {
				if !strings.Contains(stdout, sub) {
					t.Errorf("help output does not mention %q", sub)
				}
			}
		})
	}
}

func TestExecuteVersionPrintsBuildVersion(t *testing.T) {
	withVersion(t, "7.6.5-test")
	setArgs(t, "op-forward", "version")
	out := captureStdio(t)
	err := Execute()
	stdout, stderr := out()

	if err != nil {
		t.Fatalf("Execute(version) = %v, want nil", err)
	}
	if stdout != "op-forward 7.6.5-test\n" {
		t.Errorf("stdout = %q, want %q", stdout, "op-forward 7.6.5-test\n")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// An unrecognized command must be reported as an error and must not run any
// real subcommand. The temp HOME doubles as a tripwire: if the dispatcher fell
// through to install or service, it would create files under it.
func TestExecuteUnknownCommandFailsWithoutFallingThrough(t *testing.T) {
	home := tempHome(t)

	for _, name := range []string{"frobnicate", "Serve", "serve2", "install-shim"} {
		t.Run(name, func(t *testing.T) {
			setArgs(t, "op-forward", name)
			out := captureStdio(t)
			err := Execute()
			stdout, stderr := out()

			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Execute(%s) error = %v, want an error naming the command", name, err)
			}
			if !strings.Contains(stderr, "Unknown command: "+name) {
				t.Errorf("stderr = %q, want an 'Unknown command' notice", stderr)
			}
			if !strings.Contains(stdout, "Usage:") {
				t.Errorf("usage should follow an unknown command, stdout = %q", stdout)
			}
		})
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("unknown commands created files under HOME: %v", entries)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
