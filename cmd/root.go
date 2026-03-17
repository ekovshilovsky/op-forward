package cmd

import (
	"fmt"
	"os"
)

// Version is set at build time via ldflags:
//
//	go build -ldflags="-X github.com/ekovshilovsky/op-forward/cmd.Version=0.3.0"
var Version = "dev"

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
  op-forward serve [--port PORT]    Start the host daemon
  op-forward install [--port PORT]  Install the op shim on the remote side
  op-forward proxy [args...]        Forward an op command to the host daemon
  op-forward service install        Install as a launchd daemon (macOS)
  op-forward service uninstall      Remove the launchd daemon
  op-forward update                 Update to the latest release
  op-forward version                Print version

The host daemon executes 'op' commands locally, triggering biometric
authentication (Touch ID) through the 1Password desktop app. Commands
are forwarded from remote environments via SSH tunnel.

Architecture:
  Remote VM: op shim → HTTP → SSH RemoteForward → Host daemon → op CLI → Touch ID

`, Version)
}
