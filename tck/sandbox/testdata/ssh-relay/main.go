// Command ssh-relay is the fake runtime's SSH agent relay: it listens on a
// socket inside the fake "sandbox", enforces com.docker.sandbox/ssh-agent@1
// between that socket and the backing agent, and exits when killed. The
// suite judges it only through what reaches the backing agent, exactly as
// it judges a real runtime's relay; -broken removes one rule at a time so
// each check can be shown to fail when its rule is absent.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	msgFailure           = 5
	msgSuccess           = 6
	msgRequestIdentities = 11
	msgSignRequest       = 13
	msgExtension         = 27
	msgUserauthRequest   = 50
)

type config struct {
	Phase        string   `json:"phase"`
	Unrestricted *bool    `json:"unrestricted"`
	Sign         []string `json:"sign"`
	Authenticate []string `json:"authenticate"`
}

func (c config) bounded() bool { return c.Unrestricted != nil && !*c.Unrestricted }

type relay struct {
	backing string
	cfg     config
	known   map[string][]ssh.PublicKey // host name -> host keys
	broken  string
}

// binding is a verified session binding on one connection.
type binding struct {
	hostKey    ssh.PublicKey
	sessionID  []byte
	forwarding bool
}

func main() {
	listen := flag.String("listen", "", "socket to serve inside the sandbox")
	backing := flag.String("backing", "", "backing agent socket")
	cfgPath := flag.String("config", "", "the ssh-agent entry as JSON")
	knownPath := flag.String("known-hosts", "", "host keys for authenticate destinations")
	broken := flag.String("broken", "", "one rule to break")
	flag.Parse()

	var cfg config
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Fatal(err)
	}
	known, err := readKnownHosts(*knownPath)
	if err != nil {
		log.Fatal(err)
	}
	r := &relay{backing: *backing, cfg: cfg, known: known, broken: *broken}
	l, err := net.Listen("unix", *listen)
	if err != nil {
		log.Fatal(err)
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go r.serve(conn)
	}
}

func readKnownHosts(path string) (map[string][]ssh.PublicKey, error) {
	out := map[string][]ssh.PublicKey{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		name, key, ok := strings.Cut(strings.TrimSpace(s.Text()), " ")
		if !ok {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("known host %s: %w", name, err)
		}
		out[name] = append(out[name], pub)
	}
	return out, s.Err()
}

func (r *relay) serve(client net.Conn) {
	defer client.Close()
	var upstream net.Conn
	defer func() {
		if upstream != nil {
			upstream.Close()
		}
	}()
	forward := func(msg []byte) ([]byte, error) {
		if upstream == nil {
			c, err := net.Dial("unix", r.backing)
			if err != nil {
				return nil, err
			}
			upstream = c
		}
		if err := writeFrame(upstream, msg); err != nil {
			return nil, err
		}
		return readFrame(upstream)
	}
	var bound *binding
	for {
		msg, err := readFrame(client)
		if err != nil {
			return
		}
		reply, err := r.handle(msg, &bound, forward)
		if err != nil {
			reply = []byte{msgFailure}
		}
		if err := writeFrame(client, reply); err != nil {
			return
		}
	}
}

func (r *relay) handle(msg []byte, bound **binding, forward func([]byte) ([]byte, error)) ([]byte, error) {
	if len(msg) == 0 {
		return []byte{msgFailure}, nil
	}
	if r.broken == "ssh-agent-relays-everything" {
		return forward(msg)
	}
	if r.broken == "ssh-agent-forwards-constrained-rsa" && msg[0] == 24 {
		return forward(msg)
	}
	switch msg[0] {
	case msgRequestIdentities:
		return forward(msg)
	case msgSignRequest:
		if r.signAllowed(msg[1:], *bound) {
			return forward(msg)
		}
		return []byte{msgFailure}, nil
	case msgExtension:
		name, rest, ok := sshString(msg[1:])
		if ok && string(name) == "session-bind@openssh.com" {
			b, err := r.verifyBinding(rest)
			if err != nil {
				return []byte{msgFailure}, nil
			}
			*bound = b
			return []byte{msgSuccess}, nil
		}
	}
	if r.broken == "ssh-agent-forwards-refused" {
		// A relay that answers failure but talks to the backing agent on
		// the way: the request count shows it.
		_, _ = forward([]byte{msgRequestIdentities})
	}
	return []byte{msgFailure}, nil
}

func (r *relay) verifyBinding(body []byte) (*binding, error) {
	var b struct {
		HostKey    []byte
		SessionID  []byte
		Signature  []byte
		Forwarding bool
	}
	if err := ssh.Unmarshal(body, &b); err != nil {
		return nil, err
	}
	hostKey, err := ssh.ParsePublicKey(b.HostKey)
	if err != nil {
		return nil, err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(b.Signature, &sig); err != nil {
		return nil, err
	}
	if r.broken != "trusts-unverified-binding" {
		if err := hostKey.Verify(b.SessionID, &sig); err != nil {
			return nil, err
		}
	}
	return &binding{hostKey: hostKey, sessionID: b.SessionID, forwarding: b.Forwarding}, nil
}

func (r *relay) signAllowed(body []byte, bound *binding) bool {
	requestedKey, rest, ok := sshString(body)
	if !ok {
		return false
	}
	data, rest, ok := sshString(rest)
	if !ok || len(rest) != 4 {
		return false
	}
	if !r.cfg.bounded() {
		return r.broken != "ssh-agent-drops-sign"
	}
	if ns, ok := sshsigNamespace(data); ok {
		switch r.broken {
		case "ignores-sign-bounds":
			return true
		case "drops-bound-signature":
			return false
		}
		return slices.Contains(r.cfg.Sign, ns)
	}
	if login, ok := parseLogin(data, requestedKey, r.broken == "trusts-invalid-login-key"); ok {
		switch r.broken {
		case "ignores-login-bounds":
			return true
		case "drops-bound-login":
			return false
		}
		return r.loginAllowed(login, bound)
	}
	return r.broken == "forwards-unclassified"
}

type login struct {
	sessionID []byte
	user      string
	method    string
	hostKey   []byte // publickey-hostbound-v00@openssh.com only
}

func (r *relay) loginAllowed(l login, b *binding) bool {
	if b == nil {
		return false
	}
	if b.forwarding && r.broken != "trusts-forwarding-binding" {
		return false
	}
	if !bytes.Equal(l.sessionID, b.sessionID) && r.broken != "ignores-session-id" {
		return false
	}
	if l.method == "publickey-hostbound-v00@openssh.com" && !bytes.Equal(l.hostKey, b.hostKey.Marshal()) && r.broken != "ignores-hostbound-key" {
		return false
	}
	for _, d := range r.cfg.Authenticate {
		user, host, hasUser := strings.Cut(d, "@")
		if !hasUser {
			host, user = user, ""
			if r.broken == "restricts-host-only-user" {
				user = "git"
			}
		}
		if user != "" && user != l.user && r.broken != "ignores-login-user" {
			continue
		}
		if r.broken == "trusts-any-host-key" || slices.ContainsFunc(r.known[host], func(k ssh.PublicKey) bool {
			return bytes.Equal(k.Marshal(), b.hostKey.Marshal())
		}) {
			return true
		}
	}
	return false
}

func sshsigNamespace(data []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(data, []byte("SSHSIG"))
	if !ok {
		return "", false
	}
	ns, _, ok := sshString(rest)
	return string(ns), ok
}

func parseLogin(data, requestedKey []byte, trustInvalid bool) (login, bool) {
	sid, rest, ok := sshString(data)
	if !ok || len(rest) == 0 || rest[0] != msgUserauthRequest {
		return login{}, false
	}
	user, rest, ok := sshString(rest[1:])
	if !ok {
		return login{}, false
	}
	service, rest, ok := sshString(rest)
	if !ok || string(service) != "ssh-connection" {
		return login{}, false
	}
	method, rest, ok := sshString(rest)
	if !ok || (string(method) != "publickey" && string(method) != "publickey-hostbound-v00@openssh.com") || len(rest) == 0 || rest[0] != 1 {
		return login{}, false
	}
	algorithm, rest, ok := sshString(rest[1:])
	if !ok || len(algorithm) == 0 {
		return login{}, false
	}
	key, rest, ok := sshString(rest)
	if !ok {
		return login{}, false
	}
	if !trustInvalid {
		if !bytes.Equal(key, requestedKey) {
			return login{}, false
		}
		publicKey, err := ssh.ParsePublicKey(key)
		if err != nil || string(algorithm) != publicKey.Type() {
			return login{}, false
		}
	}
	l := login{sessionID: sid, user: string(user), method: string(method)}
	if l.method == "publickey-hostbound-v00@openssh.com" {
		l.hostKey, rest, ok = sshString(rest)
		if !ok || len(l.hostKey) == 0 {
			return login{}, false
		}
	}
	return l, len(rest) == 0
}

func sshString(b []byte) (value, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(len(b)-4) < uint64(n) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}

func readFrame(r io.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint32(head[:]))
	_, err := io.ReadFull(r, msg)
	return msg, err
}

func writeFrame(w io.Writer, msg []byte) error {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(msg)))
	_, err := w.Write(append(head[:], msg...))
	return err
}
