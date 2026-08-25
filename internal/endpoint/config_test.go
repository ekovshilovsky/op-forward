package endpoint

import "testing"

func TestForDialUsesHostAndPortWhenAddrEmpty(t *testing.T) {
	ep, err := ForDial("", "host.docker.internal", 20000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "tcp://host.docker.internal:20000" {
		t.Fatalf("ForDial() = %q", got)
	}
}

func TestForDialAddrWins(t *testing.T) {
	ep, err := ForDial("unix:///run/op-forward.sock", "host.docker.internal", 20000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "unix:///run/op-forward.sock" {
		t.Fatalf("ForDial() = %q", got)
	}
}

func TestForDialRejectsMalformedAddr(t *testing.T) {
	if _, err := ForDial("127.0.0.1:18340", "h", 1); err == nil {
		t.Fatal("ForDial() accepted an address without a scheme")
	}
}

func TestForListenDefaultsToLoopbackTCP(t *testing.T) {
	ep, err := ForListen("", 20000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "tcp://127.0.0.1:20000" {
		t.Fatalf("ForListen() = %q", got)
	}
}

func TestForListenExplicitWins(t *testing.T) {
	ep, err := ForListen("unix:///run/op-forward.sock", 20000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "unix:///run/op-forward.sock" {
		t.Fatalf("ForListen() = %q", got)
	}
}

func TestIntFromEnv(t *testing.T) {
	const key = "OP_FORWARD_TEST_INT"
	cases := map[string]int{"": 7, "12": 12, "garbage": 7, "0": 7, "-3": 7, "1": 1}
	for raw, want := range cases {
		t.Setenv(key, raw)
		if got := IntFromEnv(key, 7, 1); got != want {
			t.Errorf("IntFromEnv(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestPortFromEnvRejectsZeroAndGarbage(t *testing.T) {
	for _, raw := range []string{"not-a-port", "0", "-1"} {
		t.Setenv(EnvPort, raw)
		if got := PortFromEnv(); got != DefaultPort {
			t.Errorf("PortFromEnv() with %q = %d, want %d", raw, got, DefaultPort)
		}
	}
}

func TestHostFromEnv(t *testing.T) {
	t.Setenv(EnvHost, "")
	if got := HostFromEnv(); got != DefaultHost {
		t.Fatalf("HostFromEnv() default = %q", got)
	}
	t.Setenv(EnvHost, "host.docker.internal")
	if got := HostFromEnv(); got != "host.docker.internal" {
		t.Fatalf("HostFromEnv() = %q", got)
	}
}
