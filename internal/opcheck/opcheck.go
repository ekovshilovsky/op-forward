// Package opcheck inspects the 1Password CLI integration on the machine it
// runs on and classifies what it finds. The daemon exposes the result at
// /health/op and "op-forward doctor" renders it, so both callers share one
// implementation and cannot disagree about what "healthy" means.
//
// The package exists because of a real incident. On a macOS host using the
// 1Password desktop-app integration, "op whoami" exited 1 with "account is not
// signed in" while the forwarding chain was fine, and an hour of debugging
// went into the chain. Measured afterwards, the real root cause was that the
// desktop app was LOCKED, never signed out. With the app locked:
//
//   - "op account list" still succeeds in well under a second (metadata does
//     not need an unlocked app);
//   - "op vault list" and the other authenticated reads HANG with no output at
//     all, and only exit 1 after about a minute of op's own patience;
//   - "op whoami" exits 1 immediately with "account is not signed in".
//
// So whoami reports a locked app with signed-out wording and cannot tell the
// two states apart, which is why nothing in this package may ever use it as a
// signal. A locked app is recognizable only by its behavior: metadata works
// and an authenticated read produces no answer. A Touch ID approval unlocks
// the app, after which every command works.
//
// The report carries booleans, counts, versions, and classification strings
// only. It never contains vault names, item data, account identifiers, or
// raw op output, because it is served by a diagnostic endpoint.
package opcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/executor"
)

// ShimSignature is the text every op-forward shim carries in its header
// comment. Locate uses it to avoid mistaking the shim for the real op: on a
// client machine "op" on PATH is the shim, and running it would forward the
// diagnostic command back to the daemon (and could raise a prompt there).
const ShimSignature = "op-forward shim"

// Integration values describe whether the op CLI could answer a metadata
// query. They say nothing about whether a session is authenticated.
const (
	// IntegrationUnavailable means no real op binary was found, so nothing
	// could be asked.
	IntegrationUnavailable = "unavailable"
	// IntegrationReachable means "op account list" completed successfully.
	IntegrationReachable = "reachable"
	// IntegrationUnreachable means op was found but "op account list" failed
	// or timed out, typically because the desktop app is not running or the
	// CLI integration is switched off.
	IntegrationUnreachable = "unreachable"
)

// Session values distinguish three situations that a single boolean would
// conflate. NotProbed in particular must never be read as healthy.
const (
	// SessionNotProbed means no authenticated command was run, so the report
	// holds metadata only.
	SessionNotProbed = "not_probed"
	// SessionHealthy means an authenticated read succeeded.
	SessionHealthy = "healthy"
	// SessionFailed means an authenticated read was attempted and failed;
	// SessionReason says how.
	SessionFailed = "failed"
)

// Refusal values explain why a located op binary is not executed.
const (
	// RefusalNotNativeExecutable means the binary found on PATH is not a
	// native Windows executable (op.exe). A .bat or .cmd file would be run
	// through cmd.exe, which re-parses arguments, so the daemon refuses it.
	RefusalNotNativeExecutable = "not_native_executable"
)

// extensionPattern bounds what may be reported as the refused extension.
var extensionPattern = regexp.MustCompile(`^\.[0-9A-Za-z]{1,8}$`)

// Reasons classify a failed authenticated probe. The exit status alone
// decides success or failure; the reason is derived from stderr text on a
// best-effort basis to choose the right advice, and anything unrecognized
// falls back to ReasonOpError rather than being guessed at.
const (
	ReasonNone        = ""
	ReasonNotSignedIn = "not_signed_in"
	ReasonAuthDenied  = "authorization_denied"
	ReasonAppLocked   = "app_locked"
	ReasonTimeout     = "timeout"
	ReasonOpError     = "op_error"
)

// stderrCapBytes bounds how much of a command's stderr is retained for
// classification.
const stderrCapBytes = 4096

// Default timeouts. Metadata queries answer from local state (about 60ms
// measured) and fail fast.
//
// The authenticated probe must be long enough for a person who is looking at
// the host to unlock the 1Password app or approve a biometric prompt, and
// short enough that a health check is not left hanging. Measured, a locked
// app never answers at all, so the whole timeout is always spent in exactly
// the failure this probe exists to find; every extra second is a second the
// operator waits for nothing. Twenty seconds covers a Touch ID approval or a
// typed unlock with margin while keeping the wait bounded; it replaces an
// earlier 30 seconds that bought no additional successes. The probe only ever
// runs when explicitly requested, and callers announce the wait up front.
const (
	DefaultMetadataTimeout = 5 * time.Second
	DefaultProbeTimeout    = 20 * time.Second
)

// Report is the classified state of the op integration. Field names are a
// stable contract consumed by "op-forward doctor --json" and /health/op.
type Report struct {
	// OpPresent reports whether a real (non-shim) op binary was found on PATH.
	OpPresent bool `json:"op_present"`
	// OpRefusal is one of the Refusal* values when an op binary was found
	// but the daemon would refuse to execute it, and empty otherwise. A
	// refused binary is not run by Check either, so every other field then
	// describes "not examined" rather than "failed".
	OpRefusal string `json:"op_refusal,omitempty"`
	// OpExtension is the extension of the refused binary (for example
	// ".cmd"), kept only when it is short and alphanumeric so unexpected
	// input can never flow into the diagnostic. Empty unless OpRefusal is set.
	OpExtension string `json:"op_extension,omitempty"`
	// OpVersion is the version printed by "op --version", or empty when the
	// binary could not be executed.
	OpVersion string `json:"op_version"`
	// Integration is one of the Integration* values.
	Integration string `json:"integration"`
	// AccountCount is the number of accounts "op account list" returned, or
	// null when it could not be determined.
	AccountCount *int `json:"account_count"`
	// Session is one of the Session* values.
	Session string `json:"session"`
	// SessionReason is one of the Reason* values; empty unless Session is
	// SessionFailed.
	SessionReason string `json:"session_reason"`
}

// Options control a Check run.
type Options struct {
	// Authenticated enables the authenticated session probe. It is off by
	// default because a real authenticated command can raise a biometric
	// prompt on the host (see Probe).
	Authenticated bool
	// MetadataTimeout bounds each metadata command; zero selects
	// DefaultMetadataTimeout.
	MetadataTimeout time.Duration
	// ProbeTimeout bounds the authenticated probe; zero selects
	// DefaultProbeTimeout.
	ProbeTimeout time.Duration
}

// probeArgs is the authenticated read used as the session probe.
//
// DO NOT REPLACE THIS WITH "op whoami". See the package documentation: with the
// 1Password app locked, whoami exits 1 immediately with "account is not signed
// in", the same wording as a genuinely signed-out account, so it cannot tell a
// locked app from a signed-out one, and it reported "not signed in" on a host
// whose forwarding chain was fine. "vault list" goes through the same
// authorization path as real commands, so it fails (or hangs, for a locked
// app) exactly when real commands would. Its output names vaults, so the probe
// counts the bytes of stdout and discards them unread.
var probeArgs = []string{"vault", "list", "--format=json"}

// versionPattern bounds what may be reported as the op version, so that
// unexpected output from the binary can never flow into the diagnostic.
var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)

// Locate returns the path of the real op binary on PATH. Entries that are
// op-forward shims are skipped, as are relative PATH entries, which would
// make the answer depend on the working directory.
//
// On Windows the binary is op.exe, so each directory is searched for the
// names produced by appending the PATHEXT extensions, in PATHEXT order, which
// mirrors how exec.LookPath resolves "op" when the daemon later runs it.
func Locate() (string, bool) {
	names := candidateNames(runtime.GOOS, os.Getenv("PATHEXT"))
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err != nil || !isExecutable(runtime.GOOS, info) {
				continue
			}
			if IsShim(candidate) {
				continue
			}
			return candidate, true
		}
	}
	return "", false
}

// defaultPathExt is the Windows default used when PATHEXT is unset or empty.
const defaultPathExt = ".COM;.EXE;.BAT;.CMD"

// candidateNames lists the file names under which the op binary may exist in
// a PATH directory on goos. Unix has the single name "op". Windows has no
// extensionless executables, so the names are "op" plus each PATHEXT entry.
func candidateNames(goos, pathExt string) []string {
	if goos != "windows" {
		return []string{"op"}
	}
	if strings.TrimSpace(pathExt) == "" {
		pathExt = defaultPathExt
	}
	var names []string
	for _, ext := range strings.Split(pathExt, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		names = append(names, "op"+strings.ToLower(ext))
	}
	return names
}

// isExecutable reports whether a located file can plausibly be run as op.
// Unix requires a regular file with an execute bit. Windows has no execute
// bit (Go reports the same synthetic permissions for every file) and decides
// executability by extension, which candidateNames has already applied, so
// anything that is not a directory qualifies. Windows reparse points such as
// winget links or execution aliases are not "regular" files, which is why the
// test is looser there.
func isExecutable(goos string, info fs.FileInfo) bool {
	if goos == "windows" {
		return !info.IsDir()
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// IsShim reports whether the file at path is an op-forward shim, judged by
// the signature in its header. Only the first kilobyte is read, which is
// enough for the header and harmless on a large binary.
func IsShim(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- path is a candidate op location resolved from the invoking user's own PATH or home directory, read only to look for the shim signature
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 1024)
	// A short read is expected for small scripts; whatever was read is
	// examined, and a read error simply yields an empty header.
	n, _ := io.ReadFull(f, head)
	return strings.Contains(string(head[:n]), ShimSignature)
}

// Check gathers the op integration report. It never returns an error: every
// failure is itself a classification carried in the Report.
func Check(ctx context.Context, opts Options) Report {
	return check(ctx, opts, executor.ValidateResolvedPath)
}

// check implements Check with the resolved-path validator injected, so the
// refusal path can be exercised on platforms whose real validator accepts
// every path.
func check(ctx context.Context, opts Options, validate func(string) error) Report {
	if opts.MetadataTimeout <= 0 {
		opts.MetadataTimeout = DefaultMetadataTimeout
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = DefaultProbeTimeout
	}

	rep := Report{
		Integration: IntegrationUnavailable,
		Session:     SessionNotProbed,
	}
	opPath, ok := Locate()
	if !ok {
		return rep
	}
	rep.OpPresent = true

	// The daemon refuses to execute some binaries it can find (on Windows,
	// anything that is not a native .exe). Locate stays a pure lookup so the
	// diagnostic can say what is there, but running the binary here would
	// execute exactly what the daemon will not, so it is reported and left
	// alone.
	if err := validate(opPath); err != nil {
		rep.OpRefusal = RefusalNotNativeExecutable
		var notNative *executor.NotNativeExecutableError
		if errors.As(err, &notNative) && extensionPattern.MatchString(notNative.Ext) {
			rep.OpExtension = strings.ToLower(notNative.Ext)
		}
		return rep
	}

	rep.OpVersion = readVersion(ctx, opPath, opts.MetadataTimeout)

	// "op account list" is metadata only: it enumerates locally configured
	// and app-integrated accounts and performs no authenticated vault read,
	// so it does not raise a biometric prompt.
	if count, answered := accountCount(ctx, opPath, opts.MetadataTimeout); answered {
		rep.Integration = IntegrationReachable
		rep.AccountCount = count
	} else {
		rep.Integration = IntegrationUnreachable
	}

	if opts.Authenticated {
		rep.Session, rep.SessionReason = probeSession(ctx, opPath, opts.ProbeTimeout, rep.Integration == IntegrationReachable)
	}
	return rep
}

// runOp executes the located op binary with a timeout. Stdin is closed
// (/dev/null) so a CLI-side password prompt fails immediately instead of
// waiting for input that can never arrive. The returned boolean reports
// whether the deadline expired.
func runOp(ctx context.Context, opPath string, timeout time.Duration, stdout io.Writer, args ...string) (stderr string, timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- opPath is the absolute path returned by Locate and has passed executor.ValidateResolvedPath in check, and every argument is a compile-time constant defined in this package; no shell is involved on any platform: unix executes the file directly, and on Windows the validator requires a native .exe because CreateProcess runs .bat and .cmd files through cmd.exe
	cmd := exec.CommandContext(ctx, opPath, args...)
	var errBuf cappedBuffer
	cmd.Stdout = stdout
	cmd.Stderr = &errBuf
	// A killed op can leave a grandchild holding the pipes open; bound how
	// long Wait lingers on them.
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	return errBuf.String(), errors.Is(ctx.Err(), context.DeadlineExceeded), err
}

// readVersion returns the sanitized output of "op --version", or empty when
// the command fails or prints something that is not a plain version string.
func readVersion(ctx context.Context, opPath string, timeout time.Duration) string {
	var out bytes.Buffer
	if _, _, err := runOp(ctx, opPath, timeout, &out, "--version"); err != nil {
		return ""
	}
	v := strings.TrimSpace(out.String())
	if !versionPattern.MatchString(v) {
		return ""
	}
	return v
}

// accountCount runs "op account list --format=json". The boolean reports
// whether the command completed successfully, which is what proves the
// integration answered. The count is nil when the output was not a JSON
// array, so the integration is known to be reachable while the number of
// accounts is not. Only the length is kept; account identifiers are
// discarded as soon as they are decoded.
func accountCount(ctx context.Context, opPath string, timeout time.Duration) (count *int, answered bool) {
	var out bytes.Buffer
	if _, _, err := runOp(ctx, opPath, timeout, &out, "account", "list", "--format=json"); err != nil {
		return nil, false
	}
	var accounts []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &accounts); err != nil {
		return nil, true
	}
	n := len(accounts)
	return &n, true
}

// probeSession runs the authenticated read and classifies the outcome. The
// exit status alone decides healthy versus failed.
//
// Prompt policy, decided after reviewing the op CLI's documented flags and
// environment variables: op offers no switch that makes a desktop-app
// authorization fail instead of prompting. OP_SERVICE_ACCOUNT_TOKEN and
// OP_CONNECT_* select different authentication back ends, --session applies
// only to the legacy sign-in flow, and closing stdin affects only CLI-side
// password prompts. The biometric dialog is raised by the 1Password app, not
// by op, so a timeout also cannot withdraw it. With no reliable
// non-interactive mode, callers must treat this probe as opt-in: Check runs
// it only when Options.Authenticated is set, which the doctor exposes as
// --interactive and /health/op exposes as probe=1.
//
// integrationReachable is whether "op account list" answered. It is what
// separates a locked app from an integration that is down (see
// classifyFailure).
func probeSession(ctx context.Context, opPath string, timeout time.Duration, integrationReachable bool) (session, reason string) {
	var stdout countingDiscard
	stderr, timedOut, err := runOp(ctx, opPath, timeout, &stdout, probeArgs...)
	if err == nil {
		return SessionHealthy, ReasonNone
	}
	silent := stdout.n == 0 && stderr == ""
	return SessionFailed, classifyFailure(stderr, timedOut, silent, integrationReachable)
}

// classifyFailure maps a failed probe to a reason.
//
// The locked-app mapping comes from measured behavior, not from op's error
// text. With the 1Password app locked, the metadata command "op account list"
// succeeds but an authenticated read hangs with no output on either stream
// until it is killed. A fast error is NOT what a locked app produces. So a
// probe that timed out in silence on a host whose account list answered is
// reported as ReasonAppLocked: the integration is up and the accounts exist,
// but the app will not authorize until someone unlocks it or approves the
// pending prompt on the host. The same silence while "op account list" also
// failed proves nothing about the app, so it stays the generic ReasonTimeout,
// as does a probe that timed out after printing something. Note that
// "op whoami" cannot make this distinction: it reports a locked app with the
// "account is not signed in" wording, indistinguishable from a signed-out one.
//
// The remaining phrases are the wording observed from op for failures that
// do return; an unrecognized message degrades to the generic ReasonOpError,
// which still means "the host-side session failed".
func classifyFailure(stderr string, timedOut, silent, integrationReachable bool) string {
	if timedOut {
		if silent && integrationReachable {
			return ReasonAppLocked
		}
		return ReasonTimeout
	}
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "not signed in"), strings.Contains(s, "not currently signed in"):
		return ReasonNotSignedIn
	case strings.Contains(s, "dismissed"), strings.Contains(s, "denied"), strings.Contains(s, "cancel"), strings.Contains(s, "authorization"):
		return ReasonAuthDenied
	case strings.Contains(s, "locked"):
		return ReasonAppLocked
	default:
		return ReasonOpError
	}
}

// countingDiscard drops everything written to it and remembers only how many
// bytes there were. The probe's stdout names vaults, so it is never retained,
// yet the classifier still needs to know whether the command said anything.
type countingDiscard struct{ n int }

func (c *countingDiscard) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// cappedBuffer keeps the first stderrCapBytes bytes written to it and
// silently drops the rest, so a misbehaving command cannot grow memory
// without bound while classification still sees the start of its message.
type cappedBuffer struct {
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := stderrCapBytes - c.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
