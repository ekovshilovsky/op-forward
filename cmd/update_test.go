package cmd

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
)

// After replacing the binary, update must not report success until the
// respawned daemon actually answers, so a launchd throttle or a crash on
// the new binary is visible to the person running the update.
func TestWaitForHealthSucceedsOnceDaemonAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ep, _ := endpoint.Parse("tcp://" + ln.Addr().String())
	// Hold the port but do not serve for a moment, then start answering.
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the probe listener: %v", err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		ln2, err := net.Listen("tcp", ep.Address)
		if err != nil {
			return
		}
		// Serve returns only when the listener is torn down at process exit,
		// so its error carries no information for the test.
		_ = http.Serve(ln2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}
		}))
	}()

	start := time.Now()
	if err := waitForHealth(ep, 5*time.Second); err != nil {
		t.Fatalf("waitForHealth() error: %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("waitForHealth returned before the daemon was serving")
	}
}

func TestWaitForHealthTimesOutWhenNothingAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ep, _ := endpoint.Parse("tcp://" + ln.Addr().String())
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the probe listener: %v", err)
	}

	if err := waitForHealth(ep, 500*time.Millisecond); err == nil {
		t.Fatal("waitForHealth() succeeded with nothing listening")
	}
}
