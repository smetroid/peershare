package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

var loopback = []string{"/ip4/127.0.0.1/tcp/0"}

func newTestHost(t *testing.T, relays []peer.AddrInfo) host.Host {
	t.Helper()
	sk, err := loadIdentity(filepath.Join(t.TempDir(), "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := newPeerHost(sk, loopback, relays)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// shareDir creates a directory holding hello.txt and a symlink pointing
// outside it.
func shareDir(t *testing.T) string {
	t.Helper()
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mountURL connects client to target and returns the local WebDAV base URL.
func mountURL(t *testing.T, client host.Host, target peer.AddrInfo) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx, target); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go forward(context.Background(), client, target.ID, ln)
	return "http://" + ln.Addr().String()
}

func do(t *testing.T, method, url string) (int, string) {
	t.Helper()
	var body io.Reader
	if method == http.MethodPut {
		body = strings.NewReader("x")
	}
	req, _ := http.NewRequest(method, url, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestShare(t *testing.T) {
	sharer, friend, stranger := newTestHost(t, nil), newTestHost(t, nil), newTestHost(t, nil)
	srv, err := serveShare(sharer, shareDir(t), map[peer.ID]bool{friend.ID(): true}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	target := peer.AddrInfo{ID: sharer.ID(), Addrs: sharer.Addrs()}

	url := mountURL(t, friend, target)
	if code, body := do(t, http.MethodGet, url+"/hello.txt"); code != 200 || body != "hi" {
		t.Errorf("allowed GET = %d %q, want 200 \"hi\"", code, body)
	}
	if code, _ := do(t, "PROPFIND", url+"/"); code != http.StatusMultiStatus {
		t.Errorf("allowed PROPFIND = %d, want 207", code)
	}
	if code, _ := do(t, http.MethodPut, url+"/new.txt"); code != http.StatusForbidden {
		t.Errorf("PUT on read-only share = %d, want 403", code)
	}
	if code, body := do(t, http.MethodGet, url+"/escape/secret.txt"); code == 200 || strings.Contains(body, "secret") {
		t.Errorf("GET through symlink escape = %d %q, want refusal", code, body)
	}

	if code, _ := do(t, http.MethodGet, mountURL(t, stranger, target)+"/hello.txt"); code == 200 {
		t.Error("peer not on the allowlist could read the share")
	}
}

func TestShareWritable(t *testing.T) {
	sharer, friend := newTestHost(t, nil), newTestHost(t, nil)
	dir := shareDir(t)
	srv, err := serveShare(sharer, dir, map[peer.ID]bool{friend.ID(): true}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	url := mountURL(t, friend, peer.AddrInfo{ID: sharer.ID(), Addrs: sharer.Addrs()})
	if code, _ := do(t, http.MethodPut, url+"/new.txt"); code != http.StatusCreated {
		t.Errorf("PUT = %d, want 201", code)
	}
	if code, _ := do(t, http.MethodPut, url+"/escape/planted.txt"); code < 400 {
		t.Errorf("PUT through symlink escape = %d, want refusal", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Error(err)
	}
}

func TestShareViaRelay(t *testing.T) {
	sharerKey, _, _ := crypto.GenerateEd25519Key(nil)
	sharerID, _ := peer.IDFromPrivateKey(sharerKey)

	r, err := libp2p.New(
		libp2p.ListenAddrStrings(loopback...),
		libp2p.ForceReachabilityPublic(),
		libp2p.EnableRelayService(relay.WithInfiniteLimits(), relay.WithACL(reserveACL{sharerID: true})),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	relays := []peer.AddrInfo{{ID: r.ID(), Addrs: r.Addrs()}}

	sharer, err := newPeerHost(sharerKey, loopback, relays)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sharer.Close() })
	friend := newTestHost(t, nil)
	srv, err := serveShare(sharer, shareDir(t), map[peer.ID]bool{friend.ID(): true}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := connectRelays(ctx, sharer, relays); err != nil {
		t.Fatal(err)
	}

	// Only relay addresses, as a peer behind NAT would publish. The
	// reservation follows the relay connection asynchronously, so retry.
	target := peer.AddrInfo{ID: sharer.ID(), Addrs: circuitAddrs(relays)}
	for friend.Connect(ctx, target) != nil {
		if ctx.Err() != nil {
			t.Fatal("could not reach sharer through relay")
		}
		time.Sleep(200 * time.Millisecond)
	}
	url := mountURL(t, friend, target)
	if code, body := do(t, http.MethodGet, url+"/hello.txt"); code != 200 || body != "hi" {
		t.Errorf("GET via relay = %d %q, want 200 \"hi\"", code, body)
	}
}

func TestTicketRoundTrip(t *testing.T) {
	h := newTestHost(t, nil)
	want := peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}
	s, err := encodeTicket(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeTicket(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || len(got.Addrs) != len(want.Addrs) {
		t.Errorf("ticket round trip = %v, want %v", got, want)
	}
}
