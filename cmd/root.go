package cmd

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// Version is set at build time via ldflags:
//
//	go build -ldflags="-X github.com/ekovshilovsky/op-forward/cmd.Version=0.3.0"
//
// Builds that skip the ldflags, such as a plain 'go install
// github.com/ekovshilovsky/op-forward@latest', still report the module
// version recorded by the Go toolchain; see resolveVersion.
var Version = "dev"

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolveVersion(Version, info, ok)
}

// resolveVersion chooses the version string the binary reports. An explicit
// ldflags value wins, since the release pipeline stamps it from the tag. When
// the ldflags default is still in place, the module version the Go toolchain
// embedded takes over: 'go install module@version' records the module version,
// and a build inside a git checkout records a tag-derived version such as
// "v1.0.0+dirty". The toolchain reports "(devel)" only when it has neither,
// which carries no more information than the default, so the default is kept.
func resolveVersion(current string, info *debug.BuildInfo, ok bool) string {
	if current != "dev" {
		return current
	}
	if !ok || info == nil {
		return current
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		return current
	}
	return strings.TrimPrefix(v, "v")
}

// ExitStatus is the error a command returns when the process must terminate
// with a specific exit code. The command has already written any diagnostic
// to stderr by the time it returns one, so the caller turns it into the
// process exit code without printing it again.
//
// The proxy relies on this for its exit-code contract: 127 tells the op shim
// that the forwarding infrastructure failed and the real op binary should be
// exec'd instead, while every other value is relayed from the forwarded op
// command unchanged. A zero status is never returned for a completed command;
// success is a nil error. Zero is used only for a help request, which
// terminates the process successfully without running the command.
type ExitStatus int

// Error describes the status for logs and test failures. Callers that map the
// status to a process exit code do not print it.
func (e ExitStatus) Error() string {
	return fmt.Sprintf("exit status %d", int(e))
}

func Execute() error {
	if len(os.Args) < 2 {
		printUsage()
		return nil
	}

	switch os.Args[1] {
	case "serve":
		return runServe()
	case "install":
		return runInstall()
	case "proxy":
		return runProxy()
	case "service":
		return runService()
	case "update":
		return runUpdate()
	case "doctor":
		return runDoctor()
	case "version":
		fmt.Printf("op-forward %s\n", Version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		return fmt.Errorf("unknown command: %s", os.Args[1])
	}
}

func printUsage() {
	fmt.Printf(`op-forward %s — Forward 1Password CLI across SSH boundaries

Usage:
  op-forward serve [--listen EP]    Start the host daemon (EP: tcp://127.0.0.1:PORT or unix:///path.sock)
  op-forward install                Install the op shim on the remote side
  op-forward proxy [args...]        Forward an op command to the host daemon (OP_FORWARD_ADDR selects the endpoint)
  op-forward service install        Install as a launchd daemon (macOS)
  op-forward service uninstall      Remove the launchd daemon
  op-forward update                 Update to the latest release
  op-forward doctor [--json] [--interactive]
                                    Diagnose the shim, tunnel, daemon, tokens and host-side 1Password session
  op-forward version                Print version

The host daemon executes 'op' commands locally, triggering biometric
authentication (Touch ID) through the 1Password desktop app. Commands
are forwarded from remote environments via SSH tunnel.

Architecture:
  Remote VM: op shim → HTTP → SSH RemoteForward → Host daemon → op CLI → Touch ID

`, Version)
}
