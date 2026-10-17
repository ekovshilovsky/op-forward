package endpoint

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skipIfRoot skips tests whose failure depends on file permissions, which the
// superuser bypasses.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("permission-dependent behavior is not enforced for uid 0")
	}
}

// ---------- listenUnix error table ----------

func TestListenUnixMkdirAllUnderRegularFile(t *testing.T) {
	blocker := shortSocketPath(t) // A path inside a private temp dir, reused as a regular file.
	if err := os.WriteFile(blocker, []byte("unrelated-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ep := Endpoint{Network: "unix", Address: filepath.Join(blocker, "sub", "s.sock")}

	ln, err := ep.Listen()
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen() succeeded with a regular file in the socket directory path")
	}
	if !strings.Contains(err.Error(), "creating socket directory") {
		t.Fatalf("Listen() error = %q, want it to name the socket directory creation", err)
	}
	data, readErr := os.ReadFile(blocker)
	if readErr != nil || string(data) != "unrelated-content" {
		t.Fatalf("blocking file was altered: %v %q", readErr, data)
	}
}

func TestListenUnixStaleSocketInUnremovableDirectory(t *testing.T) {
	skipIfRoot(t)
	sock := shortSocketPath(t)
	dir := filepath.Dir(sock)

	// Leave a stale socket behind: bound, closed, and not unlinked.
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("creating stale socket: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("closing stale listener: %v", err)
	}

	// Mode 0500 passes the private-directory check but forbids unlinking
	// entries. The cleanup registered here runs before shortSocketPath's
	// removal, restoring write access so the directory can be deleted.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	ln, err := Endpoint{Network: "unix", Address: sock}.Listen()
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen() succeeded although the stale socket could not be removed")
	}
	if !strings.Contains(err.Error(), "removing stale socket") {
		t.Fatalf("Listen() error = %q, want the stale-socket removal failure", err)
	}
	info, statErr := os.Lstat(sock)
	if statErr != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket should remain in place, Lstat = %v, %v", info, statErr)
	}
}

func TestListenUnixOverlongPathIncludesPlatformHint(t *testing.T) {
	root := filepath.Dir(shortSocketPath(t))
	// A long directory name keeps every component legal while pushing the
	// full socket path beyond sun_path on both macOS (104) and Linux (108).
	longDir := filepath.Join(root, strings.Repeat("d", 120))
	sock := filepath.Join(longDir, "s.sock")
	if len(sock) <= 104 {
		t.Fatalf("fixture path is only %d bytes, want more than 104", len(sock))
	}

	ln, err := Endpoint{Network: "unix", Address: sock}.Listen()
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen() succeeded on an over-long socket path")
	}
	for _, fragment := range []string{"listen on unix socket", "104 bytes on macOS", "shorter path"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("Listen() error = %q, want it to contain %q", err, fragment)
		}
	}
	if _, statErr := os.Lstat(sock); !os.IsNotExist(statErr) {
		t.Errorf("no socket file should exist after the failure, Lstat error = %v", statErr)
	}
}

// The real-world ordering: a world-readable directory such as /tmp together
// with an over-long path. The directory refusal comes first because it is a
// security check that must hold before binding, but the error must also carry
// the path-length hint, so fixing the configuration takes one edit rather than
// two. The fixture directory is deliberately 0755; the test above this one
// passes only because its root is private.
func TestListenUnixOverlongPathInSharedDirectoryReportsBothProblems(t *testing.T) {
	root := filepath.Dir(shortSocketPath(t))
	longDir := filepath.Join(root, strings.Repeat("d", 120))
	if err := os.Mkdir(longDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(longDir, "s.sock")

	ln, err := Endpoint{Network: "unix", Address: sock}.Listen()
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen() succeeded in a world-readable directory")
	}
	for _, fragment := range []string{"is mode 0755", "must be 0700", "104 bytes on macOS", "shorter path"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("Listen() error = %q, want it to contain %q", err, fragment)
		}
	}
	if _, statErr := os.Lstat(sock); !os.IsNotExist(statErr) {
		t.Errorf("nothing may be bound after the refusal, Lstat error = %v", statErr)
	}
}

// A short path in a shared directory gets the directory refusal alone: the
// length hint would be noise.
func TestListenUnixShortPathInSharedDirectoryOmitsTheLengthHint(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Endpoint{Network: "unix", Address: path}.Listen()
	if err == nil || !strings.Contains(err.Error(), "must be 0700") {
		t.Fatalf("Listen() error = %v, want the directory refusal", err)
	}
	if strings.Contains(err.Error(), "104") {
		t.Errorf("Listen() error = %q, must not carry the length hint for a short path", err)
	}
}

// ---------- checkPrivateDir ----------

func TestCheckPrivateDirMissingDirectory(t *testing.T) {
	missing := filepath.Join(filepath.Dir(shortSocketPath(t)), "absent")

	err := checkPrivateDir(missing)
	if err == nil || !strings.Contains(err.Error(), "checking socket directory") {
		t.Fatalf("checkPrivateDir() error = %v, want the stat failure", err)
	}
}

func TestCheckPrivateDirRegularFile(t *testing.T) {
	file := shortSocketPath(t)
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := checkPrivateDir(file)
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("checkPrivateDir() error = %v, want the not-a-directory refusal", err)
	}
}

func TestCheckPrivateDirAcceptsOwnedPrivateDirectory(t *testing.T) {
	dir := filepath.Dir(shortSocketPath(t))
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := checkPrivateDir(dir); err != nil {
		t.Fatalf("checkPrivateDir() rejected an owned 0700 directory: %v", err)
	}
}

// ---------- PeerUID on a closed connection ----------

func TestPeerUIDClosedConnection(t *testing.T) {
	sock := shortSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

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
	defer func() { _ = client.Close() }()
	server := <-accepted

	if err := server.Close(); err != nil {
		t.Fatalf("closing server connection: %v", err)
	}

	uid, ok := PeerUID(server)
	if ok {
		t.Fatalf("PeerUID() on a closed connection = (%d, true), want ok=false", uid)
	}
	if uid != 0 {
		t.Errorf("PeerUID() uid = %d on failure, want 0", uid)
	}
}
