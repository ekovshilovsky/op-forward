package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/opcheck"
)

// Exit statuses of "op-forward doctor". Warnings and skipped checks never
// change the status: they describe things worth knowing, not things that stop
// op from working.
//
//	0  every check needed for the current role is ok, warn, or skip
//	1  at least one check failed
//	2  usage error (bad flag or --role value), as for every subcommand
const (
	doctorExitFailed = 1
	doctorExitUsage  = 2
)

// Check statuses. The JSON report uses these exact strings.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
	// statusSkip marks a check that does not apply to this role or could not
	// run because a layer beneath it failed. It is never counted as a failure.
	statusSkip = "skip"
)

// Roles. A client machine reaches the daemon through the shim; a host runs
// the daemon next to the 1Password app.
const (
	roleClient = "client"
	roleHost   = "host"
	roleAuto   = "auto"
)

const (
	// doctorProbeTimeout bounds the endpoint reachability probe.
	doctorProbeTimeout = 2 * time.Second
	// doctorHealthTimeout bounds the plain /health request.
	doctorHealthTimeout = 5 * time.Second
	// doctorOpHealthTimeout bounds /health/op without an authenticated probe:
	// two metadata commands on the daemon host.
	doctorOpHealthTimeout = 20 * time.Second
	// doctorOpProbeTimeout bounds /health/op?probe=1, which runs the two
	// metadata commands, then waits for the authenticated probe to finish or
	// time out (a locked app spends the whole probe timeout), plus margin for
	// the response.
	doctorOpProbeTimeout = 2*opcheck.DefaultMetadataTimeout + opcheck.DefaultProbeTimeout + 15*time.Second
)

// doctorCheck is one line of the report. Field names are a stable contract
// for UAT and automation; Data carries check-specific structured detail made
// of booleans, counts, versions and classification strings, never secrets.
type doctorCheck struct {
	ID      string         `json:"id"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Action  string         `json:"action,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// doctorReport is the full --json document.
type doctorReport struct {
	Version     string        `json:"version"`
	Role        string        `json:"role"`
	Endpoint    string        `json:"endpoint"`
	Interactive bool          `json:"interactive"`
	Status      string        `json:"status"`
	ExitCode    int           `json:"exit_code"`
	Checks      []doctorCheck `json:"checks"`
}

// runDoctor diagnoses the op-forward chain and reports which layer, if any,
// is broken. It never runs an authenticated op command unless --interactive
// is given. It changes no user data, but by default it does one thing a pure
// read would not: when the access token is missing or expired and a valid
// refresh token exists, it performs the same /token/refresh exchange the proxy
// performs and saves the rotated tokens where the proxy saves them, so that
// the daemon_op check can run instead of being skipped (access tokens last one
// hour, so this is the common case on an idle machine). --no-refresh restores
// a strictly read-only run. Any renewal is stated in the report.
//
// The layers, from the shim outward: the binary and shim, the endpoint, the
// daemon, the token chain, and finally the host's own 1Password integration.
// A failure is reported against the lowest broken layer, and layers above a
// broken one are skipped rather than failed, so an unreachable daemon is
// never reported as a 1Password problem and vice versa.
//
// This command exists because of a misleading probe. "op whoami" reports
// "account is not signed in" on hosts that rely on the 1Password desktop app,
// even while every real command works, and that false negative sent a long
// investigation down the wrong path. See internal/opcheck.
func runDoctor() error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	addr := fs.String("addr", os.Getenv(endpoint.EnvAddr), "Daemon endpoint to check (client role): tcp://host:port or unix:///path.sock (overrides --host/--port)")
	listen := fs.String("listen", os.Getenv(endpoint.EnvListen), "Daemon endpoint to check (host role): tcp://127.0.0.1:port or unix:///path.sock (overrides --port)")
	host := fs.String("host", endpoint.HostFromEnv(), "Daemon host to check (client role)")
	port := fs.Int("port", endpoint.PortFromEnv(), "Daemon port to check")
	role := fs.String("role", roleAuto, "Role to diagnose: auto, client, or host. auto treats a machine with an op-forward shim as a client, otherwise a machine with the real op as the host")
	asJSON := fs.Bool("json", false, "Print the report as JSON")
	interactive := fs.Bool("interactive", false, "Also run an authenticated 1Password read to verify the session. This MAY RAISE A BIOMETRIC PROMPT on the host")
	noRefresh := fs.Bool("no-refresh", false, "Do not renew an expired access token. By default doctor refreshes it from the refresh token (the same /token/refresh exchange the proxy makes, which rotates and rewrites the token files); this flag makes the run strictly read-only")
	if err := parseFlags(fs, os.Args[2:]); err != nil {
		return err
	}
	if *role != roleAuto && *role != roleClient && *role != roleHost {
		fmt.Fprintf(os.Stderr, "op-forward doctor: invalid --role %q (use auto, client, or host)\n", *role)
		return ExitStatus(doctorExitUsage)
	}

	shimPath := filepath.Join(os.Getenv("HOME"), ".local", "bin", "op")
	resolvedRole := resolveRole(*role, shimPath)

	explicit := explicitFlags(fs)
	var ep endpoint.Endpoint
	var epErr error
	if resolvedRole == roleHost {
		// A host checks the endpoint its daemon binds. An endpoint typed with
		// --addr is honored here too; the OP_FORWARD_ADDR environment
		// variable is a client setting and is ignored on a host.
		target := endpointArg(*listen, explicit, "port")
		if explicit["addr"] && !explicit["listen"] {
			target = *addr
		}
		ep, epErr = endpoint.ForListen(target, *port)
	} else {
		ep, epErr = endpoint.ForDial(endpointArg(*addr, explicit, "host", "port"), *host, *port)
	}

	d := &doctor{role: resolvedRole, interactive: *interactive, noRefresh: *noRefresh, shimPath: shimPath}
	report := d.run(ep, epErr)

	if *asJSON {
		// The report holds only strings, booleans, numbers and nested maps of
		// them, so encoding cannot fail.
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		printDoctorReport(os.Stdout, report)
	}

	if report.ExitCode != 0 {
		return ExitStatus(report.ExitCode)
	}
	return nil
}

// resolveRole decides which role to diagnose. An op-forward shim marks a
// client even when a real op binary also exists on the machine, because the
// shim's fallback target makes that common and a client must not be judged
// by its local, unsigned-in fallback copy of op. Without a shim, a real op
// on PATH marks the host that runs the daemon.
func resolveRole(requested, shimPath string) string {
	if requested != roleAuto {
		return requested
	}
	if opcheck.IsShim(shimPath) {
		return roleClient
	}
	if _, found := opcheck.Locate(); found {
		return roleHost
	}
	return roleClient
}

// doctor holds the run configuration shared by the individual checks.
type doctor struct {
	role        string
	interactive bool
	// noRefresh forbids renewing the access token, keeping the run read-only.
	noRefresh bool
	shimPath  string
	// renewal records what this run did about the access token, so the
	// daemon_op and tokens checks describe the same fact in the same words.
	renewal renewalState
	// renewalFailure is the finding when renewal is renewalFailed.
	renewalFailure opComponent
	// probeTimeout bounds the local authenticated probe in the host role; zero
	// selects opcheck.DefaultProbeTimeout. Tests shorten it.
	probeTimeout time.Duration
}

// renewalState is what a doctor run did about an unusable access token.
type renewalState int

const (
	// renewalIdle: doctor did not touch the tokens.
	renewalIdle renewalState = iota
	// renewalDone: doctor renewed the access token and saved the rotated pair.
	renewalDone
	// renewalFailed: renewal was attempted and did not complete.
	renewalFailed
	// renewalDeclined: renewal was needed but --no-refresh forbade it.
	renewalDeclined
)

// announceProbe tells the operator, on stderr, that an authenticated probe is
// starting and may wait for an action on the host. A probe against a locked
// 1Password app produces no output until its timeout, so without this the run
// looks frozen. It goes to stderr so --json output on stdout stays parseable.
func (d *doctor) announceProbe() {
	wait := d.probeTimeout
	if wait <= 0 {
		wait = opcheck.DefaultProbeTimeout
	}
	fmt.Fprintf(os.Stderr,
		"op-forward doctor: waiting up to %s for the 1Password app on the host. If it is locked or shows an approval prompt, unlock or approve it there now.\n", wait)
}

// run executes every check in layer order and assembles the report.
func (d *doctor) run(ep endpoint.Endpoint, epErr error) doctorReport {
	var checks []doctorCheck
	checks = append(checks, d.checkBinary(), d.checkShim())

	endpointCheck, reachable := d.checkEndpoint(ep, epErr)
	checks = append(checks, endpointCheck)

	if reachable {
		checks = append(checks, d.checkDaemonHealth(ep))
	} else {
		checks = append(checks, skipped("daemon_health", "not checked: the endpoint is not reachable"))
	}

	checks = append(checks, d.checkDaemonOp(ep, reachable), d.checkTokens())
	checks = append(checks, d.checkLocalOp()...)

	report := doctorReport{
		Version:     Version,
		Role:        d.role,
		Interactive: d.interactive,
		Status:      statusOK,
		Checks:      checks,
	}
	if epErr == nil {
		report.Endpoint = ep.String()
	}
	for _, c := range checks {
		switch c.Status {
		case statusFail:
			report.Status = statusFail
			report.ExitCode = doctorExitFailed
		case statusWarn:
			if report.Status == statusOK {
				report.Status = statusWarn
			}
		}
	}
	return report
}

// skipped builds a check that did not run.
func skipped(id, summary string) doctorCheck {
	return doctorCheck{ID: id, Status: statusSkip, Summary: summary}
}

// ---------- Layer 1: binary and shim ----------

func (d *doctor) checkBinary() doctorCheck {
	return doctorCheck{
		ID:      "binary",
		Status:  statusOK,
		Summary: "op-forward " + Version,
		Data:    map[string]any{"version": Version},
	}
}

// Shim states reported in the check data.
const (
	shimAbsent     = "absent"
	shimCurrent    = "current"
	shimStale      = "stale"
	shimUnstamped  = "unstamped"
	shimNotShim    = "not_shim"
	shimUnreadable = "unreadable"
)

func (d *doctor) checkShim() doctorCheck {
	const rerun = "re-run op-forward install"
	check := doctorCheck{ID: "shim", Data: map[string]any{"path": d.shimPath, "binary_version": Version}}
	set := func(state, status, summary, action string) doctorCheck {
		check.Status, check.Summary, check.Action = status, summary, action
		check.Data["state"] = state
		return check
	}

	content, err := os.ReadFile(d.shimPath) // #nosec G304 -- the shim lives at a fixed location under the invoking user's own home directory, which is the file this check exists to inspect
	switch {
	case os.IsNotExist(err):
		if d.role == roleHost {
			return set(shimAbsent, statusSkip, "no op shim installed (expected on the daemon host)", "")
		}
		return set(shimAbsent, statusWarn, "no op shim at "+d.shimPath+"; op commands on this machine are not forwarded", "run op-forward install")
	case err != nil:
		return set(shimUnreadable, statusWarn, "cannot read "+d.shimPath, "check the file permissions, then "+rerun)
	}

	text := string(content)
	if !strings.Contains(text, opcheck.ShimSignature) {
		if d.role == roleHost {
			return set(shimNotShim, statusOK, d.shimPath+" is the real op, not a shim", "")
		}
		return set(shimNotShim, statusWarn, d.shimPath+" is not an op-forward shim; op commands here are not forwarded",
			"if that is unintended, remove it and run op-forward install")
	}

	recorded, stamped := parseShimVersion(text)
	switch {
	case !stamped:
		return set(shimUnstamped, statusWarn, "shim installed by an older op-forward: unknown version, pre-dates stamping", rerun)
	case recorded != shimVersionStamp(Version):
		check.Data["shim_version"] = recorded
		return set(shimStale, statusWarn, fmt.Sprintf("shim is version %s but the binary is %s", recorded, Version), rerun)
	}
	check.Data["shim_version"] = recorded
	return set(shimCurrent, statusOK, "shim is current ("+recorded+")", "")
}

// ---------- Layer 2: endpoint and daemon ----------

// checkEndpoint resolves the endpoint and probes it. The boolean reports
// whether something is accepting connections, which gates the daemon checks.
func (d *doctor) checkEndpoint(ep endpoint.Endpoint, epErr error) (doctorCheck, bool) {
	if epErr != nil {
		return doctorCheck{
			ID:      "endpoint",
			Status:  statusFail,
			Summary: "invalid endpoint: " + epErr.Error(),
			Action:  "fix --addr/--listen or OP_FORWARD_ADDR / OP_FORWARD_LISTEN (expected tcp://host:port or unix:///path.sock)",
			Data:    map[string]any{"reachable": false},
		}, false
	}
	data := map[string]any{"endpoint": ep.String(), "network": ep.Network}
	if err := ep.Probe(doctorProbeTimeout); err != nil {
		data["reachable"] = false
		action := "start the daemon on the host (op-forward serve, or op-forward service install) and check the SSH tunnel or port forward"
		if d.role == roleHost {
			action = "start the daemon (op-forward serve, or op-forward service install)"
		}
		return doctorCheck{
			ID:      "endpoint",
			Status:  statusFail,
			Summary: "nothing is accepting connections at " + ep.String(),
			Action:  action,
			Data:    data,
		}, false
	}
	data["reachable"] = true
	return doctorCheck{ID: "endpoint", Status: statusOK, Summary: ep.String() + " accepts connections", Data: data}, true
}

// checkDaemonHealth requests the unauthenticated liveness route.
func (d *doctor) checkDaemonHealth(ep endpoint.Endpoint) doctorCheck {
	status, body, err := doctorGet(ep, "/health", "", doctorHealthTimeout)
	fail := func(summary string) doctorCheck {
		return doctorCheck{
			ID:      "daemon_health",
			Status:  statusFail,
			Summary: summary,
			Action:  "something other than the op-forward daemon is listening here, or it is unhealthy; restart the daemon and check its log",
		}
	}
	if err != nil {
		return fail("GET /health failed: " + err.Error())
	}
	var health struct {
		Status string `json:"status"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &health) != nil || health.Status != "ok" {
		return fail(fmt.Sprintf("GET /health returned HTTP %d without a healthy status", status))
	}
	return doctorCheck{ID: "daemon_health", Status: statusOK, Summary: "daemon answers /health", Data: map[string]any{"status": health.Status}}
}

// tokenFixAction is the deployment action for a client that has no usable
// credentials, worded for the role being diagnosed.
func (d *doctor) tokenFixAction() string {
	if d.role == roleHost {
		return "start the daemon (op-forward serve); it generates the token files"
	}
	return "copy refresh.token from the host (~/Library/Caches/op-forward/refresh.token) to this machine's op-forward token directory, or re-run your deployment script"
}

// renewAccessToken exchanges the refresh token for a new pair through the
// proxy's own attemptRefresh and persists the pair through the proxy's own
// writeTokenFile, access token first and then the rotated refresh token. On
// success it returns the new access token (held in memory for the request, not
// re-read from disk). On failure it records the finding in d.renewalFailure
// and returns false. Token values never reach an error message or the report.
func (d *doctor) renewAccessToken(ep endpoint.Endpoint, refreshToken string) (string, bool) {
	fail := func(summary, action string) (string, bool) {
		d.renewal = renewalFailed
		d.renewalFailure = opComponent{statusFail, summary, action}
		return "", false
	}

	tokens, err := attemptRefresh(ep.HTTPClient(doctorHealthTimeout), ep, refreshToken)
	if err != nil {
		return fail("could not renew the access token from the refresh token: "+err.Error(), d.tokenFixAction())
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return fail("could not renew the access token: the daemon's refresh answer was incomplete", d.tokenFixAction())
	}

	// The daemon has rotated the refresh token by now, so a failure to save
	// it leaves this machine holding a token the daemon no longer accepts.
	accessPath, refreshPath := proxyTokenPath(auth.AccessTokenFile), proxyTokenPath(auth.RefreshTokenFile)
	saveErr := writeTokenFile(accessPath, tokens.AccessToken, tokens.AccessExpires)
	if saveErr == nil {
		saveErr = writeTokenFile(refreshPath, tokens.RefreshToken, tokens.RefreshExpires)
	}
	if saveErr != nil {
		return fail("the access token was renewed but the rotated tokens could not be saved: "+saveErr.Error()+
			" (the daemon now expects a refresh token this machine does not have)",
			"make the token directory writable, then "+d.tokenFixAction())
	}
	d.renewal = renewalDone
	return tokens.AccessToken, true
}

// renewalFinding renders the recorded renewal failure as a check.
func (d *doctor) renewalFinding(id string) doctorCheck {
	f := d.renewalFailure
	return doctorCheck{ID: id, Status: f.status, Summary: f.summary, Action: f.action}
}

// checkDaemonOp asks the daemon what it sees of the host's op integration.
// Its answer comes from the daemon's own environment, which differs from an
// interactive shell (a launchd service has its own PATH), so it can disagree
// with the local check in useful ways. When doctor renewed the access token
// to be able to ask, the summary says so, because that changed local state.
func (d *doctor) checkDaemonOp(ep endpoint.Endpoint, reachable bool) doctorCheck {
	if !reachable {
		return skipped("daemon_op", "not checked: the daemon is not reachable")
	}
	check := d.queryDaemonOp(ep)
	if d.renewal == renewalDone {
		check.Summary = "renewed the access token from the refresh token (doctor called /token/refresh; the rotated tokens were saved to " +
			filepath.Dir(proxyTokenPath(auth.AccessTokenFile)) + "); " + check.Summary
		if check.Data == nil {
			check.Data = map[string]any{}
		}
		check.Data["token_refreshed"] = true
	}
	return check
}

func (d *doctor) queryDaemonOp(ep endpoint.Endpoint) doctorCheck {
	const id = "daemon_op"

	token, ok := readAccessTokenValue()
	if !ok {
		refreshToken, haveRefresh := readValidTokenValue(auth.RefreshTokenFile)
		switch {
		case !haveRefresh:
			return doctorCheck{ID: id, Status: statusFail,
				Summary: "not checked: no valid access token and no valid refresh token to renew it from",
				Action:  d.tokenFixAction()}
		case d.noRefresh:
			d.renewal = renewalDeclined
			c := skipped(id, "not checked: the access token is missing or expired and --no-refresh forbids renewing it")
			c.Action = "re-run doctor without --no-refresh to let it renew the token (this rewrites the token files, as the proxy does)"
			return c
		}
		if token, ok = d.renewAccessToken(ep, refreshToken); !ok {
			return d.renewalFinding(id)
		}
	}

	// In the client role --interactive is forwarded so the host-side session
	// is verified. In the host role the local check performs the probe, and
	// asking the daemon as well would raise a second prompt.
	path, timeout := "/health/op", doctorOpHealthTimeout
	if d.interactive && d.role == roleClient {
		path, timeout = "/health/op?probe=1", doctorOpProbeTimeout
		d.announceProbe()
	}
	status, body, err := doctorGet(ep, path, token, timeout)

	// A token that looks valid locally but is rejected means the daemon was
	// restarted and forgot it. Renew once and retry, as the proxy does.
	if status == http.StatusUnauthorized && d.renewal == renewalIdle && !d.noRefresh {
		if refreshToken, haveRefresh := readValidTokenValue(auth.RefreshTokenFile); haveRefresh {
			if token, ok = d.renewAccessToken(ep, refreshToken); !ok {
				return d.renewalFinding(id)
			}
			status, body, err = doctorGet(ep, path, token, timeout)
		}
	}

	switch {
	case err != nil:
		return doctorCheck{ID: id, Status: statusFail, Summary: "GET /health/op failed: " + err.Error(),
			Action: "check the daemon log; if the request timed out, an authorization prompt on the host may be waiting for approval"}
	case status == http.StatusUnauthorized && d.renewal == renewalDone:
		return doctorCheck{ID: id, Status: statusWarn, Summary: "daemon rejected the access token it had just issued",
			Action: "check that the endpoint reaches a single daemon instance (two daemons or a load balancer would explain this), then re-run doctor"}
	case status == http.StatusUnauthorized:
		return doctorCheck{ID: id, Status: statusWarn, Summary: "daemon rejected the access token",
			Action: "the daemon was probably restarted; re-run doctor without --no-refresh so it refreshes the token, or redeploy refresh.token from the host"}
	case status == http.StatusNotFound:
		return doctorCheck{ID: id, Status: statusWarn, Summary: "daemon predates /health/op, so its op integration cannot be inspected remotely",
			Action: "update the daemon host to this op-forward version (op-forward update) and restart the daemon"}
	case status == http.StatusConflict:
		return doctorCheck{ID: id, Status: statusWarn, Summary: "another authenticated probe is already running on the host",
			Action: "wait for it to finish (or answer its prompt on the host), then re-run doctor"}
	case status != http.StatusOK:
		return doctorCheck{ID: id, Status: statusFail, Summary: fmt.Sprintf("GET /health/op returned HTTP %d", status),
			Action: "check the daemon log"}
	}

	var rep opcheck.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		return doctorCheck{ID: id, Status: statusFail, Summary: "GET /health/op returned an unreadable body",
			Action: "the daemon and this binary may be different versions; update both"}
	}
	cli, integ, sess := describeOp(rep, "the daemon host")
	return foldOpComponents(id, rep, cli, integ, sess)
}

// ---------- Layer 3: tokens ----------

// tokenState is what the token check records about one token file. It never
// holds the token value, only whether the file is usable and when it expires.
type tokenState struct {
	Present bool   `json:"present"`
	Valid   bool   `json:"valid"`
	Expires string `json:"expires,omitempty"`
}

// inspectToken reads a token file and keeps only non-secret facts about it.
func inspectToken(path string) tokenState {
	if _, err := os.Stat(path); err != nil {
		return tokenState{}
	}
	t, err := auth.LoadFromPath(path)
	if err != nil {
		return tokenState{Present: true}
	}
	return tokenState{Present: true, Valid: t.IsValid(), Expires: t.Expires.UTC().Format(time.RFC3339)}
}

// readAccessTokenValue returns a currently valid access token for
// authenticating a request, falling back to the legacy session.token exactly
// as the proxy does. The value is used only as a request header and is never
// stored in a report.
func readAccessTokenValue() (string, bool) {
	for _, name := range []string{auth.AccessTokenFile, auth.LegacyTokenFile} {
		if t, err := auth.LoadFromPath(proxyTokenPath(name)); err == nil && t.IsValid() {
			return t.Value, true
		}
	}
	return "", false
}

// readValidTokenValue returns the value of the named token file when it is
// present and unexpired. The value is only ever placed in a request header.
func readValidTokenValue(name string) (string, bool) {
	t, err := auth.LoadFromPath(proxyTokenPath(name))
	if err != nil || !t.IsValid() {
		return "", false
	}
	return t.Value, true
}

func (d *doctor) checkTokens() doctorCheck {
	access := inspectToken(proxyTokenPath(auth.AccessTokenFile))
	refresh := inspectToken(proxyTokenPath(auth.RefreshTokenFile))
	legacy := inspectToken(proxyTokenPath(auth.LegacyTokenFile))
	check := doctorCheck{
		ID:   "tokens",
		Data: map[string]any{"access": access, "refresh": refresh, "legacy": legacy},
	}

	fix := d.tokenFixAction()

	switch {
	case d.renewal == renewalFailed:
		// Same status, wording and action as the daemon_op check, which
		// reports the identical fact.
		f := d.renewalFailure
		check.Status, check.Summary, check.Action = f.status, f.summary, f.action
	case d.renewal == renewalDone:
		check.Status = statusOK
		check.Summary = "access token was missing or expired; doctor renewed it from the refresh token just now and saved the rotated tokens"
		check.Data["token_refreshed"] = true
	case d.renewal == renewalDeclined:
		check.Status = statusOK
		check.Summary = "access token is missing or expired; --no-refresh was given so doctor did not renew it (the proxy renews it from the refresh token on next use)"
	case access.Valid && refresh.Valid:
		check.Status, check.Summary = statusOK, "access and refresh tokens are valid"
	case refresh.Valid:
		check.Status, check.Summary = statusOK, "access token is missing or expired and was not renewed by this run; the proxy renews it from the refresh token on next use"
	case access.Valid || legacy.Valid:
		check.Status = statusWarn
		check.Summary = "no valid refresh token: the access token cannot be renewed when it expires (access tokens last one hour)"
		check.Action = fix
	default:
		check.Status = statusFail
		check.Summary = "no usable token files: op commands cannot authenticate to the daemon"
		check.Action = fix
	}
	return check
}

// ---------- Layer 4: host-side op ----------

// checkLocalOp runs the host-side checks in the host role and reports them as
// skipped in the client role, where the daemon's view (daemon_op) is the
// authoritative one.
func (d *doctor) checkLocalOp() []doctorCheck {
	if d.role != roleHost {
		note := "client role: op runs on the daemon host, see daemon_op"
		return []doctorCheck{skipped("op_cli", note), skipped("op_integration", note), skipped("op_session", note)}
	}
	if d.interactive {
		d.announceProbe()
	}
	rep := opcheck.Check(context.Background(), opcheck.Options{Authenticated: d.interactive, ProbeTimeout: d.probeTimeout})
	cli, integ, sess := describeOp(rep, "this host")
	return []doctorCheck{
		cli.asCheck("op_cli", map[string]any{"op_present": rep.OpPresent, "op_version": rep.OpVersion}),
		integ.asCheck("op_integration", map[string]any{"integration": rep.Integration, "account_count": rep.AccountCount}),
		sess.asCheck("op_session", map[string]any{"session": rep.Session, "session_reason": rep.SessionReason}),
	}
}

// opComponent is the classified outcome of one aspect of the op report.
type opComponent struct {
	status, summary, action string
}

func (c opComponent) asCheck(id string, data map[string]any) doctorCheck {
	return doctorCheck{ID: id, Status: c.status, Summary: c.summary, Action: c.action, Data: data}
}

// describeOp turns an opcheck report into user-facing classifications. It is
// shared by the local host checks and the daemon_op check so the two cannot
// word or judge the same facts differently. where names the machine ("this
// host" or "the daemon host") for the messages.
func describeOp(rep opcheck.Report, where string) (cli, integ, sess opComponent) {
	cli = describeCLI(rep, where)
	integ = describeIntegration(rep, where)
	sess = describeSession(rep, where)
	return cli, integ, sess
}

func describeCLI(rep opcheck.Report, where string) opComponent {
	switch {
	case !rep.OpPresent:
		return opComponent{statusFail, "op CLI not found on the PATH of " + where,
			"install the 1Password CLI and make sure it is on the PATH of the process that runs the daemon (a launchd service uses the PATH in its plist; re-run op-forward service install)"}
	case rep.OpVersion == "":
		return opComponent{statusFail, "op was found on " + where + " but op --version failed or printed unexpected output",
			"run op --version in a terminal on the host and repair the installation"}
	}
	return opComponent{statusOK, "op " + rep.OpVersion, ""}
}

func describeIntegration(rep opcheck.Report, where string) opComponent {
	switch rep.Integration {
	case opcheck.IntegrationUnavailable:
		return opComponent{statusSkip, "not checked: no op CLI", ""}
	case opcheck.IntegrationUnreachable:
		return opComponent{statusFail, "op account list failed on " + where + ": the 1Password integration did not answer",
			"open and unlock the 1Password desktop app on the host and turn on Settings > Developer > Integrate with 1Password CLI"}
	}
	switch {
	case rep.AccountCount == nil:
		return opComponent{statusOK, "op account list answered (account count unavailable)", ""}
	case *rep.AccountCount == 0:
		return opComponent{statusWarn, "op account list answered but listed no accounts",
			"with desktop-app integration this can still be healthy; verify with op-forward doctor --interactive, otherwise sign in to the 1Password app on the host"}
	}
	return opComponent{statusOK, fmt.Sprintf("op account list answered (%d account(s))", *rep.AccountCount), ""}
}

// sessionFailure maps a probe failure reason to its explanation and the
// host-side action. Every entry says the problem is on the host, because the
// failed probe itself proves the forwarding chain delivered the request.
var sessionFailure = map[string][2]string{
	opcheck.ReasonNotSignedIn: {"1Password is not signed in on the host",
		"unlock the 1Password desktop app on the host and confirm Settings > Developer > Integrate with 1Password CLI is on"},
	opcheck.ReasonAuthDenied: {"the authorization prompt on the host was dismissed or denied",
		"re-run op-forward doctor --interactive and approve the prompt on the host"},
	// A locked app answers metadata queries but never answers an authenticated
	// read, so the probe is recognized by that silence (see
	// opcheck.classifyFailure). op itself reports this state as "not signed
	// in", which is why the wording here is deliberately specific.
	opcheck.ReasonAppLocked: {"the 1Password app on the host is locked or an approval is pending there",
		"unlock the 1Password app on the host (or approve the pending prompt there), then retry"},
	opcheck.ReasonTimeout: {"the 1Password integration on the host did not answer in time",
		"make sure the 1Password app is running with Settings > Developer > Integrate with 1Password CLI turned on, then retry"},
	opcheck.ReasonOpError: {"op reported an error",
		"run op vault list in a terminal on the host to see the error"},
}

func describeSession(rep opcheck.Report, where string) opComponent {
	switch rep.Session {
	case opcheck.SessionHealthy:
		return opComponent{statusOK, "authenticated read succeeded: the session is healthy", ""}
	case opcheck.SessionFailed:
		reason, ok := sessionFailure[rep.SessionReason]
		if !ok {
			reason = sessionFailure[opcheck.ReasonOpError]
		}
		return opComponent{statusFail,
			"host-side 1Password session check failed on " + where + ": " + reason[0] + " (the op-forward chain delivered the request)",
			reason[1]}
	}
	return opComponent{statusSkip,
		"authenticated session probe not run: by default doctor only reads metadata, because a real authenticated read can raise a biometric prompt on the host",
		"to verify the session run op-forward doctor --interactive (this may raise a prompt on the host)"}
}

// foldOpComponents merges the three classifications of the daemon's report
// into the single daemon_op check. The status and action come from the most
// severe component (fail, then warn, then ok, then skip; the first wins a
// tie). A skipped session probe never outranks an ok component, but when
// nothing is worse its note on how to run the probe becomes the action.
func foldOpComponents(id string, rep opcheck.Report, parts ...opComponent) doctorCheck {
	rank := map[string]int{statusSkip: 0, statusOK: 1, statusWarn: 2, statusFail: 3}
	worst := parts[0]
	summaries := make([]string, 0, len(parts))
	for _, p := range parts {
		summaries = append(summaries, p.summary)
		if rank[p.status] > rank[worst.status] {
			worst = p
		}
	}
	action := worst.action
	if worst.status == statusOK {
		for _, p := range parts {
			if p.status == statusSkip && p.action != "" {
				action = p.action
			}
		}
	}
	return doctorCheck{
		ID:      id,
		Status:  worst.status,
		Summary: strings.Join(summaries, "; "),
		Action:  action,
		Data: map[string]any{
			"op_present":     rep.OpPresent,
			"op_version":     rep.OpVersion,
			"integration":    rep.Integration,
			"account_count":  rep.AccountCount,
			"session":        rep.Session,
			"session_reason": rep.SessionReason,
		},
	}
}

// ---------- HTTP ----------

// doctorGet performs a GET against the daemon and returns the status and
// body. A bearer token is attached only when one is given.
func doctorGet(ep endpoint.Endpoint, path, bearer string, timeout time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.BaseURL()+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("User-Agent", "op-forward/"+Version)
	req.Header.Set("X-Client-Version", Version)

	resp, err := ep.HTTPClient(timeout).Do(req)
	if err != nil {
		return 0, nil, err
	}
	// The body is read in full below; a close failure on a fully consumed
	// response carries no actionable information.
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// ---------- Rendering ----------

// printDoctorReport renders the human-readable report.
func printDoctorReport(w io.Writer, r doctorReport) {
	endpointText := r.Endpoint
	if endpointText == "" {
		endpointText = "(unresolved)"
	}
	_, _ = fmt.Fprintf(w, "op-forward doctor %s — role: %s, endpoint: %s\n\n", r.Version, r.Role, endpointText)
	labels := map[string]string{statusOK: "ok  ", statusWarn: "WARN", statusFail: "FAIL", statusSkip: "skip"}
	for _, c := range r.Checks {
		_, _ = fmt.Fprintf(w, "  [%s] %-15s %s\n", labels[c.Status], c.ID, c.Summary)
		if c.Action != "" {
			_, _ = fmt.Fprintf(w, "         %-15s -> %s\n", "", c.Action)
		}
	}
	fails, warns := 0, 0
	for _, c := range r.Checks {
		switch c.Status {
		case statusFail:
			fails++
		case statusWarn:
			warns++
		}
	}
	_, _ = fmt.Fprintf(w, "\nResult: %s (%d failure(s), %d warning(s))\n", r.Status, fails, warns)
}
