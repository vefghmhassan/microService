// Package sshx runs commands on provisioned servers as root over SSH.
package sshx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Keypair is the panel's own key, generated once and injected into every
// server through the provider API and cloud-init.
type Keypair struct {
	Signer    ssh.Signer
	PublicKey string // authorized_keys format
}

// LoadOrCreateKey reads dir/id_ed25519, creating it on first use.
func LoadOrCreateKey(dir string) (*Keypair, error) {
	path := filepath.Join(dir, "id_ed25519")
	pemBytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "microserver")
		if err != nil {
			return nil, err
		}
		pemBytes = pem.EncodeToMemory(block)
		if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " microserver"
	return &Keypair{Signer: signer, PublicKey: pub}, nil
}

// Client is one SSH connection.
type Client struct {
	c       *ssh.Client
	HostKey string // authorized_keys form of the key the server presented
}

// Dial connects as root. knownHostKey pins the server's key (trust on first
// use): empty accepts any key and reports it back in Client.HostKey.
func Dial(ctx context.Context, host string, port int, kp *Keypair, knownHostKey string) (*Client, error) {
	var mu sync.Mutex
	var seen string
	cfg := &ssh.ClientConfig{
		User:    "root",
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(kp.Signer)},
		Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			mu.Lock()
			seen = got
			mu.Unlock()
			if knownHostKey != "" && knownHostKey != got {
				return fmt.Errorf("host key changed for %s (expected %s)", host, knownHostKey)
			}
			return nil
		},
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Client{c: ssh.NewClient(sc, chans, reqs), HostKey: seen}, nil
}

// DialRetry keeps trying until the server's sshd accepts our key; fresh
// servers take a while before cloud-init has installed it.
func DialRetry(ctx context.Context, host string, port int, kp *Keypair, knownHostKey string, timeout time.Duration) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		c, err := Dial(ctx, host, port, kp, knownHostKey)
		if err == nil {
			return c, nil
		}
		last = err
		if strings.Contains(err.Error(), "host key changed") {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("ssh %s: %w", host, last)
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Client) Close() error { return c.c.Close() }

// DialContext opens a TCP connection from the server's side, so services bound
// to the server's localhost (the 3x-ui panel) are reachable through the tunnel.
func (c *Client) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	return c.c.Dial(network, addr)
}

// Run executes cmd, streaming each output line to logLine, and returns the
// combined output. A non-zero exit is an error.
func (c *Client) Run(ctx context.Context, cmd string, logLine func(string)) (string, error) {
	sess, err := c.c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	pr, pw := io.Pipe()
	sess.Stdout = pw
	sess.Stderr = pw
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			out.WriteString(line + "\n")
			if logLine != nil {
				logLine(stripANSI(line))
			}
		}
	}()

	if err := sess.Start(cmd); err != nil {
		pw.Close()
		<-done
		return "", err
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- sess.Wait() }()
	select {
	case err = <-waitErr:
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		err = ctx.Err()
	}
	pw.Close()
	<-done
	return out.String(), err
}

// ReadFile returns a remote file's contents.
func (c *Client) ReadFile(ctx context.Context, path string) (string, error) {
	return c.Run(ctx, "cat "+shellQuote(path), nil)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ShellQuote is exported for callers building commands.
func ShellQuote(s string) string { return shellQuote(s) }

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseEnv reads KEY=VALUE lines (as written to /etc/x-ui/install-result.env),
// accepting optional quotes.
func ParseEnv(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}
