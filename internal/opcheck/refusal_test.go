package opcheck

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ekovshilovsky/op-forward/internal/executor"
)

// installLocatableOp places a file where Locate will find it on any platform:
// the first PATHEXT-derived name on Windows, plain "op" elsewhere. The content
// is irrelevant because these tests refuse the binary before it can run.
func installLocatableOp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := candidateNames(runtime.GOOS, os.Getenv("PATHEXT"))[0]
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not a real op\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return path
}

func refuseWith(err error) func(string) error {
	return func(string) error { return err }
}

// A refused op is reported as present, labelled with why, and not examined.
func TestCheckReportsRefusedOpWithoutRunningIt(t *testing.T) {
	installLocatableOp(t)
	refusal := &executor.NotNativeExecutableError{Name: "op.CMD", Ext: ".CMD"}

	rep := check(context.Background(), Options{Authenticated: true}, refuseWith(refusal))

	if !rep.OpPresent {
		t.Error("OpPresent = false, want true: the binary was found")
	}
	if rep.OpRefusal != RefusalNotNativeExecutable {
		t.Errorf("OpRefusal = %q, want %q", rep.OpRefusal, RefusalNotNativeExecutable)
	}
	if rep.OpExtension != ".cmd" {
		t.Errorf("OpExtension = %q, want the lower-cased extension .cmd", rep.OpExtension)
	}
	if rep.OpVersion != "" || rep.AccountCount != nil {
		t.Errorf("version %q / accounts %v were reported for a binary that must not run", rep.OpVersion, rep.AccountCount)
	}
	if rep.Integration != IntegrationUnavailable {
		t.Errorf("Integration = %q, want %q", rep.Integration, IntegrationUnavailable)
	}
	// Even with an authenticated probe requested, nothing was attempted.
	if rep.Session != SessionNotProbed || rep.SessionReason != "" {
		t.Errorf("Session = %q (%q), want %q", rep.Session, rep.SessionReason, SessionNotProbed)
	}
}

// The extension reaches a diagnostic that remote clients display, so anything
// that is not a short alphanumeric extension is dropped.
func TestCheckDropsUnexpectedExtensions(t *testing.T) {
	installLocatableOp(t)
	for _, ext := range []string{"", ".", ".exe ", ".cmd;calc", ".waytoolongext", "cmd", ".c\x1b[31md"} {
		rep := check(context.Background(), Options{}, refuseWith(&executor.NotNativeExecutableError{Name: "op" + ext, Ext: ext}))
		if rep.OpRefusal != RefusalNotNativeExecutable {
			t.Errorf("ext %q: OpRefusal = %q, want the refusal kept", ext, rep.OpRefusal)
		}
		if rep.OpExtension != "" {
			t.Errorf("ext %q: OpExtension = %q, want it dropped", ext, rep.OpExtension)
		}
	}
}

// Any validator failure refuses the binary, not only the typed one.
func TestCheckRefusesOnAnyValidatorError(t *testing.T) {
	installLocatableOp(t)
	rep := check(context.Background(), Options{}, refuseWith(errors.New("some other policy")))
	if rep.OpRefusal != RefusalNotNativeExecutable || rep.OpExtension != "" {
		t.Errorf("OpRefusal = %q, OpExtension = %q", rep.OpRefusal, rep.OpExtension)
	}
}

// The refusal fields are additive: a normal report must serialize exactly as
// it did before they existed, so older doctors and scripts see no change.
func TestReportJSONOmitsRefusalFieldsWhenEmpty(t *testing.T) {
	data, err := json.Marshal(Report{OpPresent: true, Integration: IntegrationReachable, Session: SessionNotProbed})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "op_refusal") || strings.Contains(string(data), "op_extension") {
		t.Errorf("report JSON = %s, want no refusal fields when nothing is refused", data)
	}

	data, err = json.Marshal(Report{OpPresent: true, OpRefusal: RefusalNotNativeExecutable, OpExtension: ".cmd"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"op_refusal":"not_native_executable"`, `"op_extension":".cmd"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("report JSON = %s, want %s", data, want)
		}
	}
}
