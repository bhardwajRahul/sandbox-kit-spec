package sandbox

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// backingAgent is the SSH agent the suite hands a runtime as the one
// holding the user's keys. It is the suite's own, holding a key generated
// for one check, so what a sandbox does to the agent behind the relay is
// judged against an agent the suite can inspect afterwards — and without
// a user's keys ever being in play.
type backingAgent struct {
	socket   string
	dir      string
	listener net.Listener
	keyring  agent.ExtendedAgent
	public   ssh.PublicKey

	// requests counts every request frame that reached the agent, of any
	// type, counted on the wire rather than in the agent's methods: the
	// agent library answers some request types (smartcard loading,
	// malformed requests) without calling a method, and a relay passing
	// those on must still be seen passing them.
	requests     atomic.Int64
	requestTypes [256]atomic.Int64

	// signed records, in order, what each sign request that reached the
	// agent was for, as classifySignature names it. The bounds are judged
	// here: a relay that let a refused signature through shows up in this
	// log whatever the sandbox was told.
	signedMu sync.Mutex
	signed   []string

	// Close accepted connections too: ServeAgent can block indefinitely on
	// an idle connection after the listener stops accepting new ones.
	connsMu sync.Mutex
	conns   map[net.Conn]struct{}
	done    chan struct{}

	wg sync.WaitGroup
}

// startBackingAgent serves a fresh keyring holding one new ed25519 key.
func startBackingAgent() (*backingAgent, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	keyring, ok := agent.NewKeyring().(agent.ExtendedAgent)
	if !ok {
		return nil, errors.New("the agent keyring does not implement the extended agent interface")
	}
	if err := keyring.Add(agent.AddedKey{PrivateKey: private, Comment: "kit-tck"}); err != nil {
		return nil, err
	}
	// A Unix socket path is capped near 104 bytes on macOS; test temp
	// directories routinely exceed that, so the socket lives directly
	// under the system temp root.
	dir, err := os.MkdirTemp("/tmp", "kit-tck-agent-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	a := &backingAgent{socket: socket, dir: dir, listener: listener, keyring: keyring, public: signer.PublicKey(), conns: make(map[net.Conn]struct{}), done: make(chan struct{})}
	a.wg.Add(1)
	go a.serve()
	return a, nil
}

func (a *backingAgent) serve() {
	defer a.wg.Done()
	defer close(a.done)
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			return
		}
		a.connsMu.Lock()
		a.conns[conn] = struct{}{}
		a.wg.Add(1)
		a.connsMu.Unlock()
		go func() {
			defer a.wg.Done()
			defer func() {
				_ = conn.Close()
				a.connsMu.Lock()
				delete(a.conns, conn)
				a.connsMu.Unlock()
			}()
			wire := struct {
				io.Reader
				io.Writer
			}{&frameCounter{r: conn, n: &a.requests, types: &a.requestTypes}, conn}
			_ = agent.ServeAgent(recordingAgent{ExtendedAgent: a.keyring, record: a.record}, wire)
		}()
	}
}

// Socket is the path a runtime is handed as the backing agent.
func (a *backingAgent) Socket() string { return a.socket }

// PublicKey is the key's authorized_keys line without a comment, the form
// agents list it in.
func (a *backingAgent) PublicKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(a.public)))
}

// Holds reports whether the agent still holds its key, read by the suite
// directly rather than through the sandbox. A locked agent lists nothing,
// so a lock the relay let through reads as a lost key too.
func (a *backingAgent) Holds() (bool, error) {
	keys, err := a.keyring.List()
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if string(k.Marshal()) == string(a.public.Marshal()) {
			return true, nil
		}
	}
	return false, nil
}

// Requests is how many request frames have reached the agent so far.
func (a *backingAgent) Requests() int64 { return a.requests.Load() }

func (a *backingAgent) RequestsOfType(kind byte) int64 { return a.requestTypes[kind].Load() }

// Signed returns what each sign request that reached the agent was for.
func (a *backingAgent) Signed() []string {
	a.signedMu.Lock()
	defer a.signedMu.Unlock()
	return append([]string(nil), a.signed...)
}

func (a *backingAgent) record(data []byte) {
	a.signedMu.Lock()
	defer a.signedMu.Unlock()
	a.signed = append(a.signed, classifySignature(data))
}

// Verify reports whether signature, as the probe printed it in hex, is
// the agent's key's signature over data.
func (a *backingAgent) Verify(dataHex, signatureHex string) error {
	data, err := hex.DecodeString(dataHex)
	if err != nil {
		return fmt.Errorf("signed data is not hex: %w", err)
	}
	raw, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("signature is not hex: %w", err)
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(raw, &sig); err != nil {
		return fmt.Errorf("signature does not decode: %w", err)
	}
	return a.public.Verify(data, &sig)
}

// Close stops serving and removes the socket.
func (a *backingAgent) Close() error {
	err := a.listener.Close()
	<-a.done
	a.connsMu.Lock()
	for conn := range a.conns {
		err = errors.Join(err, conn.Close())
	}
	a.connsMu.Unlock()
	a.wg.Wait()
	return errors.Join(err, os.RemoveAll(a.dir))
}

func (a *backingAgent) String() string { return fmt.Sprintf("kit-tck backing agent at %s", a.socket) }

// classifySignature names what signed data is for, as the capability page
// defines it: "sshsig <namespace>" for a namespaced signature, "login
// <user>" for a login signature, and "other" for anything else.
func classifySignature(data []byte) string {
	if rest, ok := bytes.CutPrefix(data, []byte("SSHSIG")); ok {
		if ns, _, ok := sshString(rest); ok {
			return "sshsig " + string(ns)
		}
		return "other"
	}
	_, rest, ok := sshString(data)
	if !ok || len(rest) == 0 || rest[0] != 50 {
		return "other"
	}
	user, _, ok := sshString(rest[1:])
	if !ok {
		return "other"
	}
	return "login " + string(user)
}

// sshString reads one SSH wire-format string.
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

// recordingAgent records the data of every signature it makes on its way
// to the keyring.
type recordingAgent struct {
	agent.ExtendedAgent
	record func(data []byte)
}

func (r recordingAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	r.record(data)
	return r.ExtendedAgent.Sign(key, data)
}

func (r recordingAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	r.record(data)
	return r.ExtendedAgent.SignWithFlags(key, data, flags)
}

// frameCounter counts agent-protocol frames (a four-byte length, then
// that many bytes) as they are read, however the reads split them.
type frameCounter struct {
	r         io.Reader
	n         *atomic.Int64
	types     *[256]atomic.Int64
	firstBody bool
	header    [4]byte
	have      int    // header bytes seen of the current frame
	left      uint32 // body bytes still to come
}

func (f *frameCounter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	for b := p[:n]; len(b) > 0; {
		if f.left > 0 {
			if f.firstBody {
				f.types[b[0]].Add(1)
				f.firstBody = false
			}
			step := min(uint32(len(b)), f.left)
			f.left -= step
			b = b[step:]
			continue
		}
		f.header[f.have] = b[0]
		f.have++
		b = b[1:]
		if f.have == 4 {
			f.have = 0
			f.left = binary.BigEndian.Uint32(f.header[:])
			f.firstBody = true
			f.n.Add(1)
		}
	}
	return n, err
}
