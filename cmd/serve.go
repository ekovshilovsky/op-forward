package cmd

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/daemon"
)

const DefaultPort = 18340

func runServe() error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", getPort(), "Port to listen on")
	fs.Parse(os.Args[2:])

	// Load or generate auth token
	token, isNew, err := auth.LoadOrGenerate()
	if err != nil {
		return fmt.Errorf("token setup: %w", err)
	}

	tokenPath, _ := auth.TokenPath()
	if isNew {
		fmt.Printf("Token generated (no valid existing token found)\n")
	} else {
		fmt.Printf("Token reused from existing file (expires %s)\n", token.Expires.Format("2006-01-02T15:04:05-07:00"))
	}
	fmt.Printf("Token written to: %s\n", tokenPath)
	fmt.Printf("Token expires at: %s\n", token.Expires.Format("2006-01-02T15:04:05-07:00"))

	if err := token.Save(); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}

	fmt.Printf("Starting daemon on 127.0.0.1:%d\n", *port)

	server := daemon.New(token, *port, Version)
	return server.Start()
}

func getPort() int {
	if p := os.Getenv("OP_FORWARD_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	return DefaultPort
}
