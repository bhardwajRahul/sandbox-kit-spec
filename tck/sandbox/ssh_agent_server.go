package sandbox

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	// sshTestServer is the destination the bounded fixture names. It is
	// under .example, which is reserved, so no real server answers: the
	// suite only ever presents this server's host key in a binding.
	sshTestServer = "kit-tck.example"
	// sshTestUser is the account the fixture's destination names.
	sshTestUser = "git"
)

// sshServer is a server identity the suite can bind sessions to: a host
// key and a name. Nothing listens; a session binding is the host key's
// signature over a session identifier, which the suite can make itself.
type sshServer struct {
	name   string
	signer ssh.Signer
}

func newSSHServer(name string) (*sshServer, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	return &sshServer{name: name, signer: signer}, nil
}

// knownHosts writes a known_hosts file naming these servers, which is
// what the runtime is handed as the host keys it may match bindings to.
func knownHosts(dir string, servers ...*sshServer) (string, error) {
	var b strings.Builder
	for _, s := range servers {
		b.WriteString(s.name + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.signer.PublicKey()))) + "\n")
	}
	path := filepath.Join(dir, "known_hosts")
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

// newSessionID is a random session identifier, standing in for the
// exchange hash of a real key exchange.
func newSessionID() []byte {
	sid := make([]byte, 32)
	_, _ = rand.Read(sid)
	return sid
}

// binding is the body of a session-bind@openssh.com request, hex encoded
// for the probe: the host key, the session identifier, the host key's
// signature over it, and the forwarding flag.
func (s *sshServer) binding(sid []byte, forwarding bool) (string, error) {
	return bindingSignedBy(s.signer.PublicKey(), s.signer, sid, forwarding)
}

// bindingSignedBy lets the host key a binding names differ from the key
// that signed it: a binding claiming the allowed server's key but signed
// by another is the forgery a relay must catch by verifying.
func bindingSignedBy(hostKey ssh.PublicKey, signer ssh.Signer, sid []byte, forwarding bool) (string, error) {
	sig, err := signer.Sign(rand.Reader, sid)
	if err != nil {
		return "", err
	}
	body := ssh.Marshal(struct {
		HostKey    []byte
		SessionID  []byte
		Signature  []byte
		Forwarding bool
	}{hostKey.Marshal(), sid, ssh.Marshal(sig), forwarding})
	return hex.EncodeToString(body), nil
}
