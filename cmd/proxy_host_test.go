package cmd

import "testing"

func TestGetProxyHost(t *testing.T) {
	t.Setenv("OP_FORWARD_HOST", "")
	if got := getProxyHost(); got != "127.0.0.1" {
		t.Fatalf("default host = %q, want 127.0.0.1", got)
	}

	t.Setenv("OP_FORWARD_HOST", "host.docker.internal")
	if got := getProxyHost(); got != "host.docker.internal" {
		t.Fatalf("override host = %q, want host.docker.internal", got)
	}
}
