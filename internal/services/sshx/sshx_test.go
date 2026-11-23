package sshx

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// serve accepts one SSH connection. A silent server completes the handshake
// and then never answers anything, like a machine deleted at its provider.
func serve(t *testing.T, silent bool) (string, int) {
	t.Helper()
	kp, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(kp.Signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil || silent {
			return
		}
		go ssh.DiscardRequests(reqs)
		for c := range chans {
			_ = c.Reject(ssh.Prohibited, "no")
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func dialTest(t *testing.T, silent bool) *Client {
	t.Helper()
	old := keepAliveEvery
	keepAliveEvery = 50 * time.Millisecond
	t.Cleanup(func() { keepAliveEvery = old })
	kp, err := LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host, port := serve(t, silent)
	c, err := Dial(context.Background(), host, port, kp, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func closedWithin(c *Client, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		_ = c.c.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestKeepAliveClosesDeadConnection(t *testing.T) {
	if c := dialTest(t, true); !closedWithin(c, 5*time.Second) {
		t.Fatal("connection to a silent server stayed open")
	}
}

func TestKeepAliveLeavesLiveConnection(t *testing.T) {
	if c := dialTest(t, false); closedWithin(c, 500*time.Millisecond) {
		t.Fatal("connection to a live server was closed")
	}
}
