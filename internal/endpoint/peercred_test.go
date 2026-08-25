package endpoint

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPeerUIDMatchesConnectingProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("peer credentials are only extracted on linux and darwin")
	}
	dir, err := os.MkdirTemp("", "opf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()

	uid, ok := PeerUID(server)
	if !ok {
		t.Fatal("PeerUID() reported no credentials on a unix connection")
	}
	if uid != os.Getuid() {
		t.Fatalf("PeerUID() = %d, want %d", uid, os.Getuid())
	}
}

func TestPeerUIDUnavailableOnTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()

	if _, ok := PeerUID(server); ok {
		t.Fatal("PeerUID() claimed credentials on a TCP connection")
	}
}
