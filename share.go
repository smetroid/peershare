package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/net/webdav"
)

func runShare(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	identity := fs.String("identity", defaultIdentityPath(), "private key file (created if missing)")
	writable := fs.Bool("rw", false, "allow allowed peers to write, rename and delete")
	var allow, relayAddrs, listen, announceAddrs listFlag
	fs.Var(&allow, "allow", "peer ID allowed to access the share (repeatable, required)")
	fs.Var(&relayAddrs, "relay", "relay multiaddr ending in /p2p/<id> (repeatable)")
	fs.Var(&listen, "listen", "libp2p listen multiaddr (repeatable)")
	fs.Var(&announceAddrs, "announce", "extra multiaddr to put in the ticket, e.g. /dns4/share.example.com/tcp/443/wss (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: peershare share [flags] <dir>")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	if len(allow) == 0 {
		return errors.New("at least one -allow <peer ID> is required")
	}
	allowed, err := parsePeerIDs(allow)
	if err != nil {
		return err
	}
	relays, err := parseRelays(relayAddrs)
	if err != nil {
		return err
	}
	announce, err := parseAnnounce(announceAddrs)
	if err != nil {
		return err
	}
	if len(listen) == 0 {
		listen = defaultListen
	}
	sk, err := loadIdentity(*identity)
	if err != nil {
		return err
	}

	h, err := newPeerHost(sk, listen, relays)
	if err != nil {
		return err
	}
	defer h.Close()

	srv, err := serveShare(h, fs.Arg(0), allowed, *writable)
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := connectRelays(ctx, h, relays); err != nil {
		return err
	}
	addrs := append(append(announce, h.Addrs()...), circuitAddrs(relays)...)
	ticket, err := encodeTicket(peer.AddrInfo{ID: h.ID(), Addrs: addrs})
	if err != nil {
		return err
	}
	mode := "read-only"
	if *writable {
		mode = "read-write"
	}
	fmt.Printf("Sharing %s (%s) as %s\n", fs.Arg(0), mode, h.ID())
	fmt.Printf("Give this to the allowed peer:\n\n  peershare mount %s\n\n", ticket)

	<-ctx.Done()
	return nil
}

// serveShare serves dir as WebDAV to streams from allowed peers. libp2p
// authenticates the remote peer's key during the handshake, so the peer ID
// checked here is proven, not claimed.
func serveShare(h host.Host, dir string, allowed map[peer.ID]bool, writable bool) (*http.Server, error) {
	root, err := filepath.Abs(dir)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(root); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	ln := newStreamListener()
	h.SetStreamHandler(protocolID, func(s network.Stream) {
		remote := s.Conn().RemotePeer()
		if !allowed[remote] {
			log.Printf("rejected peer %s", remote)
			s.Reset()
			return
		}
		ln.push(s)
	})

	srv := &http.Server{
		Handler:           newShareHandler(root, writable),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go srv.Serve(ln)
	return srv, nil
}

func newShareHandler(root string, writable bool) http.Handler {
	dav := &webdav.Handler{
		FileSystem: rootedFS{root: root},
		LockSystem: webdav.NewMemLS(),
	}
	if writable {
		return dav
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
			dav.ServeHTTP(w, r)
		default:
			http.Error(w, "share is read-only", http.StatusForbidden)
		}
	})
}

// rootedFS is webdav.Dir that also refuses paths resolving outside root
// through symlinks, which webdav.Dir follows.
type rootedFS struct{ root string }

func (r rootedFS) dir() webdav.Dir { return webdav.Dir(r.root) }

// check resolves the deepest existing ancestor of name, so a new file
// created under a symlinked directory is caught too.
func (r rootedFS) check(name string) error {
	p := filepath.Join(r.root, filepath.FromSlash(path.Clean("/"+name)))
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			if real != r.root && !strings.HasPrefix(real, r.root+string(filepath.Separator)) {
				return fs.ErrPermission
			}
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) || p == r.root {
			return err
		}
		p = filepath.Dir(p)
	}
}

func (r rootedFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	if err := r.check(name); err != nil {
		return err
	}
	return r.dir().Mkdir(ctx, name, perm)
}

func (r rootedFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if err := r.check(name); err != nil {
		return nil, err
	}
	return r.dir().OpenFile(ctx, name, flag, perm)
}

func (r rootedFS) RemoveAll(ctx context.Context, name string) error {
	if err := r.check(name); err != nil {
		return err
	}
	return r.dir().RemoveAll(ctx, name)
}

func (r rootedFS) Rename(ctx context.Context, oldName, newName string) error {
	if err := r.check(oldName); err != nil {
		return err
	}
	if err := r.check(newName); err != nil {
		return err
	}
	return r.dir().Rename(ctx, oldName, newName)
}

func (r rootedFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if err := r.check(name); err != nil {
		return nil, err
	}
	return r.dir().Stat(ctx, name)
}

// streamListener adapts incoming libp2p streams to a net.Listener so a
// standard http.Server can serve them.
type streamListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newStreamListener() *streamListener {
	return &streamListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *streamListener) push(s network.Stream) {
	select {
	case l.conns <- streamConn{s}:
	case <-l.closed:
		s.Reset()
	}
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *streamListener) Addr() net.Addr { return peerAddr("") }

type streamConn struct{ network.Stream }

func (c streamConn) LocalAddr() net.Addr  { return peerAddr(c.Conn().LocalPeer()) }
func (c streamConn) RemoteAddr() net.Addr { return peerAddr(c.Conn().RemotePeer()) }

type peerAddr peer.ID

func (a peerAddr) Network() string { return "libp2p" }
func (a peerAddr) String() string  { return peer.ID(a).String() }
