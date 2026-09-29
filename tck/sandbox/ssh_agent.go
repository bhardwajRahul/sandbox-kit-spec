package sandbox

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capSSHAgent             = "com.docker.sandbox/ssh-agent@1"
	fixtureSSHAgent         = "ssh-agent"
	fixtureSSHAgentOptional = "ssh-agent-optional"
	fixtureSSHAgentInstall  = "ssh-agent-install"
	fixtureSSHAgentBounded  = "ssh-agent-bounded"

	// sshAgentProbe is the workload fixture's agent-protocol client.
	sshAgentProbe = "kit-tck-ssh-agent"
)

// Agent-protocol request types the relay must answer itself, from
// OpenSSH's PROTOCOL.agent: adding keys (plain and constrained), removing
// one or all, adding and removing smartcard keys (plain and constrained),
// locking, and unlocking.
var sshAgentMutatingRequests = []int{17, 18, 19, 20, 21, 22, 23, 24, 25, 26}

// withBackingAgent runs a check against a fresh agent the suite owns, so
// every observation starts from a known key and a zero request count.
func withBackingAgent(run func(*backingAgent) []report.Finding) []report.Finding {
	a, err := startBackingAgent()
	if err != nil {
		return []report.Finding{report.Failf("start the suite's SSH agent: %v", err)}
	}
	// The suite's own socket directory: failing to remove it says nothing
	// about the runtime, so it must not become a finding against it.
	defer func() { _ = a.Close() }()
	return run(a)
}

// signOutcome is what one signing request through the sandbox came to.
type signOutcome int

const (
	signed signOutcome = iota
	refused
)

// probeSign runs one signing probe and judges its result: a signature
// the agent's key really made over the data the probe printed, or a
// refusal. Anything else — no agent at all, a signature that does not
// verify, a probe error — is a finding.
func probeSign(ctx context.Context, e *Env, id string, a *backingAgent, args ...string) (signOutcome, *report.Finding) {
	res, err := e.Adapter.Exec(ctx, id, append([]string{sshAgentProbe}, args...)...)
	if err != nil {
		return refused, failing("exec %s %s: %v", sshAgentProbe, args[0], err)
	}
	switch res.ExitCode {
	case 0:
	case 1:
		return refused, nil
	case 2:
		return refused, failing("%s %s: no agent was reachable in the sandbox", sshAgentProbe, args[0])
	default:
		return refused, failing("%s %s: exit %d: %s", sshAgentProbe, args[0], res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	if err := verifyAgentSignature(a, res.Stdout); err != nil {
		return signed, failing("%s %s returned a signature the backing agent's key did not make: %v", sshAgentProbe, args[0], err)
	}
	return signed, nil
}

func verifyAgentSignature(a *backingAgent, output string) error {
	fields := agentProbeFields(output)
	return a.Verify(fields["data"], fields["signature"])
}

func agentProbeFields(output string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			fields[k] = v
		}
	}
	return fields
}

// expectSigned asserts a request the entry admits is signed by the
// backing agent, and that the agent saw it as what it was.
func expectSigned(ctx context.Context, e *Env, id string, a *backingAgent, want string, args ...string) *report.Finding {
	outcome, f := probeSign(ctx, e, id, a, args...)
	switch {
	case f != nil:
		return f
	case outcome != signed:
		return failing("the relay refused %s, which the entry admits", want)
	}
	if got := a.Signed(); len(got) == 0 || got[len(got)-1] != want {
		return failing("the backing agent did not see %s; it saw %q", want, got)
	}
	return nil
}

// expectRefused asserts a request the entry does not admit is refused
// and never reaches the backing agent, which is judged by what the agent
// signed rather than by what the sandbox was told.
func expectRefused(ctx context.Context, e *Env, id string, a *backingAgent, what string, args ...string) *report.Finding {
	before := a.Signed()
	outcome, f := probeSign(ctx, e, id, a, args...)
	if f != nil {
		return f
	}
	after := a.Signed()
	if outcome == signed {
		return failing("the relay signed %s, which the entry does not admit", what)
	}
	if len(after) != len(before) {
		return failing("the relay refused %s, but passed it to the backing agent first (it saw %q)", what, after[len(before):])
	}
	return nil
}

// agentServes asserts the sandbox reaches the backing agent: its key is
// listed and a signature by it verifies. Both, because listing alone
// passes a relay that answers identities from a cached copy and never
// forwards a sign request.
func agentServes(ctx context.Context, e *Env, id string, a *backingAgent) *report.Finding {
	listed, f := execOutput(ctx, e, id, sshAgentProbe, "list")
	if f != nil {
		return failing("the granted agent was not reachable: %s", f.Detail)
	}
	if !strings.Contains(listed, a.PublicKey()) {
		return failing("the agent in the sandbox does not offer the backing agent's key; it listed %q", strings.TrimSpace(listed))
	}
	return expectSigned(ctx, e, id, a, "other", "sign", a.PublicKey())
}

// agentUnreachable asserts nothing in the sandbox reaches the backing
// agent: no socket announced, listing fails, and no request arrives at
// the agent — the last because a relay could fail the probe's listing
// while still carrying other traffic.
func agentUnreachable(ctx context.Context, e *Env, id string, a *backingAgent, when string) []report.Finding {
	before := a.Requests()
	var findings []report.Finding
	// Wait for the entrypoint's completed probe before judging its
	// environment: create may return before the workload starts.
	proof, f := initialAgentProof(ctx, e, id, "/var/tmp/kit-tck-workload-ssh-sock-proof", "")
	if f != nil {
		return []report.Finding{*f}
	}
	initial, f := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-workload-ssh-sock")
	if f != nil {
		return []report.Finding{*f}
	}
	if strings.TrimSpace(initial) != "" || strings.Contains(proof, "data ") || strings.Contains(proof, a.PublicKey()) {
		findings = append(findings, report.Failf("%s, yet the initial workload recorded socket %q or agent access", when, strings.TrimSpace(initial)))
	}
	socket, f := execOutput(ctx, e, id, sshAgentProbe, "socket")
	if f != nil {
		return []report.Finding{*f}
	}
	if s := strings.TrimSpace(socket); s != "" {
		findings = append(findings, report.Failf("%s, yet SSH_AUTH_SOCK is %q", when, s))
	}
	res, err := e.Adapter.Exec(ctx, id, sshAgentProbe, "list")
	if err != nil {
		return append(findings, report.Failf("exec %s list: %v", sshAgentProbe, err))
	}
	if res.ExitCode == 0 && strings.Contains(res.Stdout, a.PublicKey()) {
		findings = append(findings, report.Failf("%s, yet the sandbox listed the backing agent's key", when))
	}
	if after := a.Requests(); after != before {
		findings = append(findings, report.Failf("%s, yet %d request(s) reached the backing agent", when, after-before))
	}
	return findings
}

func sshAgentReachable(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureSSHAgent}, adapter.CreateOptions{SSHAgent: a.Socket()})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		if f := agentServes(ctx, e, id, a); f != nil {
			return []report.Finding{*f}
		}
		_, f := initialAgentsServe(ctx, e, id, a, nil)
		if f != nil {
			return []report.Finding{*f}
		}
		return nil
	})
}

func initialAgentsServe(ctx context.Context, e *Env, id string, a *backingAgent, previous map[string]string) (map[string]string, *report.Finding) {
	paths := []string{"/var/tmp/kit-tck-workload-ssh-sock"}
	if e.Claimed[capLifecycle] {
		paths = append(paths, "/var/tmp/kit-tck-startup-ssh-sock")
	}
	proofs := make(map[string]string, len(paths))
	for _, path := range paths {
		proof, f := initialAgentProof(ctx, e, id, path+"-proof", previous[path])
		if f != nil {
			return nil, f
		}
		socket, f := execOutput(ctx, e, id, "cat", path)
		if f != nil {
			return nil, failing("read initial SSH_AUTH_SOCK from %s: %s", path, f.Detail)
		}
		if strings.TrimSpace(socket) == "" {
			return nil, failing("SSH_AUTH_SOCK was absent from %s", path)
		}
		if !strings.Contains(proof, a.PublicKey()) {
			return nil, failing("the initial process at %s did not list the backing agent's key", path)
		}
		if err := verifyAgentSignature(a, proof); err != nil {
			return nil, failing("the initial process at %s did not obtain a valid backing-agent signature: %v", path, err)
		}
		proofs[path] = proof
	}
	return proofs, nil
}

// Creation can return before the entrypoint finishes its probe. Fixtures
// rename the completed record into place so an early exec never judges
// a partially written signature as a broken agent. Each probe signs fresh
// random data, so an unchanged record cannot prove access on a new boot.
func initialAgentProof(ctx context.Context, e *Env, id, path, previous string) (string, *report.Finding) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		proof, f := execOutput(ctx, e, id, "cat", path)
		if f == nil && (previous == "" || proof != previous) {
			return proof, nil
		}
		if f == nil {
			f = failing("the proof from the previous boot was not replaced")
		}
		select {
		case <-ctx.Done():
			return "", failing("read initial agent proof from %s: %s", path, f.Detail)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Initial processes run the same probes on each boot. Responses alone
// cannot prove filtering: the backing agent also records request types
// and signed data, including a rejected request that was forwarded first.
func initialProcessRestrictions(ctx context.Context, e *Env, id string, a *backingAgent, bounded bool) *report.Finding {
	var previous map[string]string
	for boot := 0; boot < 2; boot++ {
		if boot > 0 {
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return failing("stop: %v", err)
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return failing("start: %v", err)
			}
		}
		proofs, f := initialAgentsServe(ctx, e, id, a, previous)
		if f != nil {
			return f
		}
		for path, proof := range proofs {
			fields := agentProbeFields(proof)
			for _, kind := range sshAgentMutatingRequests {
				if fields[fmt.Sprintf("raw-%d", kind)] != "1" {
					return failing("%s on boot %d did not refuse operation %d", path, boot+1, kind)
				}
			}
			for _, extension := range []string{"query", "kit-tck-unknown@example.com"} {
				if fields["extension-"+extension] != "1" {
					return failing("%s on boot %d did not refuse extension %s", path, boot+1, extension)
				}
			}
			want := "0"
			if bounded {
				want = "1"
			}
			for _, probe := range []string{"other", "file", "login"} {
				if fields["probe-"+probe] != want {
					return failing("%s on boot %d: signing probe %s returned %q, want %s", path, boot+1, probe, fields["probe-"+probe], want)
				}
			}
		}
		// These probes send only forbidden extensions, never session bindings.
		for _, kind := range append(append([]int{}, sshAgentMutatingRequests...), 27) {
			if count := a.RequestsOfType(byte(kind)); count != 0 {
				return failing("initial processes forwarded %d request(s) of type %d on boot %d", count, kind, boot+1)
			}
		}
		if bounded {
			for _, request := range a.Signed() {
				if request != "sshsig git" {
					return failing("initial processes forwarded forbidden signing request %s on boot %d", request, boot+1)
				}
			}
		}
		previous = proofs
	}
	return nil
}

// sshAgentRestricted sends every request that would change the backing
// agent, and extensions nothing grants, through an unbounded entry: even
// the widest grant is a grant to use keys, not to manage the agent.
func sshAgentRestricted(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureSSHAgent}, adapter.CreateOptions{SSHAgent: a.Socket()})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		if f := initialProcessRestrictions(ctx, e, id, a, false); f != nil {
			return []report.Finding{*f}
		}
		// Reaching the agent first is what makes the refusals mean
		// anything: a sandbox that never reached it cannot change it
		// either.
		if f := agentServes(ctx, e, id, a); f != nil {
			return []report.Finding{*f}
		}
		var findings []report.Finding
		before := a.Requests()
		probes := [][]string{{"extension", "query"}, {"extension", "kit-tck-unknown@example.com"}}
		for _, t := range sshAgentMutatingRequests {
			probes = append(probes, []string{"raw", fmt.Sprint(t)})
		}
		for _, probe := range probes {
			res, err := e.Adapter.Exec(ctx, id, append([]string{sshAgentProbe}, probe...)...)
			switch {
			case err != nil:
				return append(findings, report.Failf("exec %s %s: %v", sshAgentProbe, strings.Join(probe, " "), err))
			case res.ExitCode == 0:
				findings = append(findings, report.Failf("the agent in the sandbox answered %s with success", strings.Join(probe, " ")))
			case res.ExitCode != 1:
				findings = append(findings, report.Failf("%s %s: exit %d: %s", sshAgentProbe, strings.Join(probe, " "), res.ExitCode, strings.TrimSpace(res.Stderr)))
			}
		}
		if after := a.Requests(); after != before {
			findings = append(findings, report.Failf("the relay passed requests it must answer itself: %d request(s) reached the backing agent", after-before))
		}
		switch holds, err := a.Holds(); {
		case err != nil:
			findings = append(findings, report.Failf("read the backing agent's keys: %v", err))
		case !holds:
			findings = append(findings, report.Failf("requests from the sandbox removed the backing agent's key or locked the agent"))
		}
		return findings
	})
}

// sshAgentSignaturesBounded judges the sign bound on the bounded fixture,
// which admits namespace git and logins as git to the suite's server.
func sshAgentSignaturesBounded(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureSSHAgentBounded}, adapter.CreateOptions{SSHAgent: a.Socket()})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		if f := initialProcessRestrictions(ctx, e, id, a, true); f != nil {
			return []report.Finding{*f}
		}
		if f := expectSigned(ctx, e, id, a, "sshsig git", "sshsig", "git", a.PublicKey()); f != nil {
			return []report.Finding{*f}
		}
		var findings []report.Finding
		for _, probe := range []struct {
			what string
			args []string
		}{
			{"a namespaced signature in namespace file", []string{"sshsig", "file", a.PublicKey()}},
			{"a signature for no protocol", []string{"sign", a.PublicKey()}},
		} {
			if f := expectRefused(ctx, e, id, a, probe.what, probe.args...); f != nil {
				findings = append(findings, *f)
			}
		}
		return findings
	})
}

// loginCase is one login attempt through the sandbox: a binding (or
// none) sent on the connection, then a login signature.
type loginCase struct {
	what    string
	user    string
	sid     []byte
	binding string
	hostKey string
}

func (c loginCase) args(key string) []string {
	op := "login"
	if c.hostKey != "" {
		op = "hostbound"
	}
	args := []string{op, c.user, key, hex.EncodeToString(c.sid)}
	if c.binding != "" {
		args = append(args, c.binding)
	}
	if c.hostKey != "" {
		args = append(args, c.hostKey)
	}
	return args
}

// withBoundedSandbox creates the bounded fixture with a runtime that
// trusts only the suite's two allowed servers for authenticate destinations.
func withBoundedSandbox(ctx context.Context, e *Env, run func(id string, a *backingAgent, server, hostOnly, impostor *sshServer) []report.Finding) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		server, err := newSSHServer(sshTestServer)
		if err != nil {
			return []report.Finding{report.Failf("generate the test server's host key: %v", err)}
		}
		impostor, err := newSSHServer("impostor." + sshTestServer)
		if err != nil {
			return []report.Finding{report.Failf("generate the impostor's host key: %v", err)}
		}
		hostOnly, err := newSSHServer(sshAnyUserServer)
		if err != nil {
			return []report.Finding{report.Failf("generate the host-only server key: %v", err)}
		}
		known, err := knownHosts(a.dir, server, hostOnly)
		if err != nil {
			return []report.Finding{report.Failf("write the known hosts: %v", err)}
		}
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureSSHAgentBounded},
			adapter.CreateOptions{SSHAgent: a.Socket(), SSHKnownHosts: known})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		return run(id, a, server, hostOnly, impostor)
	})
}

// judgeLogins runs an admitted login first, so a relay refusing every
// login cannot pass, then each case that must be refused.
func judgeLogins(ctx context.Context, e *Env, id string, a *backingAgent, admitted loginCase, cases []loginCase) []report.Finding {
	if f := expectSigned(ctx, e, id, a, "login "+admitted.user, admitted.args(a.PublicKey())...); f != nil {
		return []report.Finding{report.Failf("%s: %s", admitted.what, f.Detail)}
	}
	var findings []report.Finding
	for _, c := range cases {
		if f := expectRefused(ctx, e, id, a, c.what, c.args(a.PublicKey())...); f != nil {
			findings = append(findings, *f)
		}
	}
	return findings
}

func sshAgentLoginsBounded(ctx context.Context, e *Env) []report.Finding {
	return withBoundedSandbox(ctx, e, func(id string, a *backingAgent, server, hostOnly, impostor *sshServer) []report.Finding {
		bound := func(s *sshServer, sid []byte) (string, *report.Finding) {
			b, err := s.binding(sid, false)
			if err != nil {
				return "", failing("bind a session to %s: %v", s.name, err)
			}
			return b, nil
		}
		sid, other := newSessionID(), newSessionID()
		toServer, f := bound(server, sid)
		if f != nil {
			return []report.Finding{*f}
		}
		toImpostor, f := bound(impostor, sid)
		if f != nil {
			return []report.Finding{*f}
		}
		toHostOnly, f := bound(hostOnly, sid)
		if f != nil {
			return []report.Finding{*f}
		}
		for _, user := range []string{"root", "deploy"} {
			if f := expectSigned(ctx, e, id, a, "login "+user, (loginCase{"a host-only login", user, sid, toHostOnly, ""}).args(a.PublicKey())...); f != nil {
				return []report.Finding{*f}
			}
		}
		serverKey := hex.EncodeToString(server.signer.PublicKey().Marshal())
		impostorKey := hex.EncodeToString(impostor.signer.PublicKey().Marshal())
		if f := expectSigned(ctx, e, id, a, "login "+sshTestUser, (loginCase{"a hostbound login", sshTestUser, sid, toServer, serverKey}).args(a.PublicKey())...); f != nil {
			return []report.Finding{*f}
		}
		// The outer sign request still names the agent's valid key. The
		// embedded key and algorithm must describe that same public key.
		for _, probe := range []struct {
			what string
			args []string
		}{
			{"a login naming another public key", []string{"login", sshTestUser, a.PublicKey(), hex.EncodeToString(sid), toServer, impostorKey}},
			{"a login with an invalid embedded public key", []string{"login", sshTestUser, a.PublicKey(), hex.EncodeToString(sid), toServer, "00"}},
			{"a login with a mismatched key algorithm", []string{"login", sshTestUser, a.PublicKey(), hex.EncodeToString(sid), toServer, hex.EncodeToString(a.public.Marshal()), "ssh-rsa"}},
		} {
			if f := expectRefused(ctx, e, id, a, probe.what, probe.args...); f != nil {
				return []report.Finding{*f}
			}
		}
		return judgeLogins(ctx, e, id, a,
			loginCase{"a login as git to the allowed server", sshTestUser, sid, toServer, ""},
			[]loginCase{
				{"a login with no session binding", sshTestUser, sid, "", ""},
				{"a login as root to the allowed server", "root", sid, toServer, ""},
				{"a login for a session other than the bound one", sshTestUser, other, toServer, ""},
				{"a login to a server whose host key the runtime does not trust", sshTestUser, sid, toImpostor, ""},
				{"a hostbound login with a different host key", sshTestUser, sid, toServer, impostorKey},
			})
	})
}

func sshAgentBindingVerified(ctx context.Context, e *Env) []report.Finding {
	return withBoundedSandbox(ctx, e, func(id string, a *backingAgent, server, hostOnly, impostor *sshServer) []report.Finding {
		sid := newSessionID()
		valid, err := server.binding(sid, false)
		if err != nil {
			return []report.Finding{report.Failf("bind a session: %v", err)}
		}
		// Names the allowed server's host key, but the impostor signed it:
		// only verifying the signature tells it from a real binding.
		forged, err := bindingSignedBy(server.signer.PublicKey(), impostor.signer, sid, false)
		if err != nil {
			return []report.Finding{report.Failf("forge a binding: %v", err)}
		}
		forwarding, err := server.binding(sid, true)
		if err != nil {
			return []report.Finding{report.Failf("bind a forwarding session: %v", err)}
		}
		return judgeLogins(ctx, e, id, a,
			loginCase{"a login after a verified binding", sshTestUser, sid, valid, ""},
			[]loginCase{
				{"a login after a binding whose signature the named host key did not make", sshTestUser, sid, forged, ""},
				{"a login after a binding marked as forwarding", sshTestUser, sid, forwarding, ""},
			})
	})
}

func sshAgentAbsentWithoutGrant(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		// A backing agent is available; the composition never asked.
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload}, adapter.CreateOptions{SSHAgent: a.Socket()})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		findings := agentUnreachable(ctx, e, id, a, "no kit asked for the SSH agent")
		if a.Requests() != 0 {
			findings = append(findings, report.Failf("without a grant, %d requests reached the backing agent", a.Requests()))
		}
		return findings
	})
}

func sshAgentPhaseScoped(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureSSHAgentInstall}, adapter.CreateOptions{SSHAgent: a.Socket()})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		// The install hook's record comes first: an agent the install
		// phase never had would pass the scoping assertion vacuously.
		seen, f := execOutput(ctx, e, id, "cat", "/var/tmp/ssh-agent-at-install")
		if f != nil {
			return []report.Finding{*f}
		}
		if !strings.Contains(seen, a.PublicKey()) {
			return []report.Finding{report.Failf("the install hook did not reach the agent its phase was granted; it recorded %q", strings.TrimSpace(seen))}
		}
		entrypoint, f := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-workload-ssh-sock")
		if f != nil {
			return []report.Finding{report.Failf("read workload entrypoint's SSH_AUTH_SOCK: %s", f.Detail)}
		}
		if socket := strings.TrimSpace(entrypoint); socket != "" {
			return []report.Finding{report.Failf("install-only SSH agent leaked into the workload entrypoint as SSH_AUTH_SOCK=%q", socket)}
		}
		saved, f := execOutput(ctx, e, id, "cat", "/var/tmp/ssh-agent-install-sock")
		if f != nil {
			return []report.Finding{*f}
		}
		if strings.TrimSpace(saved) == "" {
			return []report.Finding{report.Failf("the install hook did not record its socket path")}
		}
		before := a.Requests()
		res, err := e.Adapter.Exec(ctx, id, sshAgentProbe, "saved", "/var/tmp/ssh-agent-install-sock")
		if err != nil || res.ExitCode != 2 || a.Requests() != before {
			return []report.Finding{report.Failf("the saved install socket %q did not close: exit %d, error %v, %d backing requests", strings.TrimSpace(saved), res.ExitCode, err, a.Requests()-before)}
		}
		return agentUnreachable(ctx, e, id, a, "the install-phase grant should have ended before the workload started")
	})
}

func sshAgentEveryBoot(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureSSHAgent}, adapter.CreateOptions{SSHAgent: a.Socket()})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		if f := agentServes(ctx, e, id, a); f != nil {
			return []report.Finding{*f}
		}
		previous, f := initialAgentsServe(ctx, e, id, a, nil)
		if f != nil {
			return []report.Finding{*f}
		}
		if err := e.Adapter.Stop(ctx, id); err != nil {
			return []report.Finding{report.Failf("stop: %v", err)}
		}
		if err := e.Adapter.Start(ctx, id); err != nil {
			return []report.Finding{report.Failf("start: %v", err)}
		}
		if f := agentServes(ctx, e, id, a); f != nil {
			return []report.Finding{report.Failf("after stop and start: %s", f.Detail)}
		}
		if _, f := initialAgentsServe(ctx, e, id, a, previous); f != nil {
			return []report.Finding{report.Failf("after stop and start: %s", f.Detail)}
		}
		return nil
	})
}

func sshAgentRequiredRefused(ctx context.Context, e *Env) []report.Finding {
	// No --ssh-agent: no backing agent is available.
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureSSHAgent}, nil)
	defer cleanup()
	var refusal *adapter.RefusedError
	switch {
	case errors.As(err, &refusal):
		if !strings.Contains(refusal.Detail, capSSHAgent) {
			return []report.Finding{report.Failf("refusal did not name %s: %s", capSSHAgent, refusal.Detail)}
		}
		return nil
	case err != nil:
		return []report.Finding{report.Failf("create failed for a reason other than refusal: %v", err)}
	default:
		return []report.Finding{report.Failf("a kit requiring the SSH agent was accepted as sandbox %s with no backing agent available", id)}
	}
}

func sshAgentOptionalSkipped(ctx context.Context, e *Env) []report.Finding {
	return withBackingAgent(func(a *backingAgent) []report.Finding {
		id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureSSHAgentOptional}, nil)
		if err != nil {
			return []report.Finding{report.Failf("an optional SSH agent entry prevented create with no backing agent available: %v", err)}
		}
		defer cleanup()
		// The suite's agent was never handed over, so it cannot have
		// been reached; what remains observable is the announced socket.
		return agentUnreachable(ctx, e, id, a, "no backing agent was available")
	})
}

// A key offered by the sandbox cannot augment the runtime's external trust.
func sshAgentDestinationKeys(ctx context.Context, e *Env) []report.Finding {
	return withBoundedSandbox(ctx, e, func(id string, a *backingAgent, server, _, impostor *sshServer) []report.Finding {
		key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(impostor.signer.PublicKey())))
		if _, f := execOutput(ctx, e, id, sshAgentProbe, "plant-known-host", sshTestServer, key); f != nil {
			return []report.Finding{*f}
		}
		sid := newSessionID()
		trusted, err := server.binding(sid, false)
		if err != nil {
			return []report.Finding{report.Failf("bind trusted server: %v", err)}
		}
		untrusted, err := impostor.binding(sid, false)
		if err != nil {
			return []report.Finding{report.Failf("bind sandbox-presented server: %v", err)}
		}
		return judgeLogins(ctx, e, id, a,
			loginCase{"externally trusted host key", sshTestUser, sid, trusted, ""},
			[]loginCase{{"host key trusted only by sandbox known_hosts", sshTestUser, sid, untrusted, ""}})
	})
}
