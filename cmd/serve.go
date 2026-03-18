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

	// Migrate legacy session.token → refresh.token if upgrading from the
	// single-token system.
	if err := auth.MigrateLegacyToken(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: legacy token migration: %v\n", err)
	}

	// Load or generate the refresh token (30-day TTL, persists across restarts).
	refreshToken, isNewRefresh, err := auth.LoadOrGenerateRefresh()
	if err != nil {
		return fmt.Errorf("refresh token setup: %w", err)
	}
	refreshPath, _ := auth.RefreshTokenPath()
	if isNewRefresh {
		fmt.Printf("Refresh token generated (no valid existing token found)\n")
	} else {
		fmt.Printf("Refresh token reused (expires %s)\n", refreshToken.Expires.Format("2006-01-02T15:04:05-07:00"))
	}
	fmt.Printf("Refresh token at: %s\n", refreshPath)

	// Always generate a fresh access token on startup (1-hour TTL).
	accessToken, err := auth.GenerateAccess()
	if err != nil {
		return fmt.Errorf("access token setup: %w", err)
	}
	accessPath, _ := auth.AccessTokenPath()
	if err := auth.SaveToPath(accessToken, accessPath); err != nil {
		return fmt.Errorf("saving access token: %w", err)
	}
	fmt.Printf("Access token written to: %s (expires %s)\n",
		accessPath, accessToken.Expires.Format("2006-01-02T15:04:05-07:00"))

	// Write the access token to the legacy session.token path so that older
	// proxy clients that read session.token continue to work until upgraded.
	if legacyPath, err := auth.LegacyTokenPath(); err == nil {
		auth.SaveToPath(accessToken, legacyPath)
	}

	fmt.Printf("Starting daemon on 127.0.0.1:%d\n", *port)

	server := daemon.New(accessToken, refreshToken, *port, Version)
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
