package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

func runMount(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mount", flag.ExitOnError)
	identity := fs.String("identity", defaultIdentityPath(), "private key file (created if missing)")
	addr := fs.String("addr", "127.0.0.1:8484", "local address for the WebDAV server")
	var relayAddrs listFlag
	fs.Var(&relayAddrs, "relay", "relay multiaddr ending in /p2p/<id> (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: peershare mount [flags] <ticket>")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	target, err := decodeTicket(fs.Arg(0))
	if err != nil {
		return err
	}
	relays, err := parseRelays(relayAddrs)
	if err != nil {
		return err
	}
	sk, err := loadIdentity(*identity)
	if err != nil {
		return err
	}

	h, err := newPeerHost(sk, defaultListen, relays)
	if err != nil {
		return err
	}
	defer h.Close()

	// Connect only keeps the ticket's addresses for two minutes. Keep them for
	// good, so a new stream can redial after the connection drops.
	h.Peerstore().AddAddrs(target.ID, target.Addrs, peerstore.PermanentAddrTTL)
	if err := h.Connect(ctx, target); err != nil {
		return fmt.Errorf("connect to %s: %w", target.ID, err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()

	fmt.Printf("Connected to %s as %s\n", target.ID, h.ID())
	fmt.Printf("Mount http://%s\n  macOS Finder: Go > Connect to Server (Cmd-K)\n", ln.Addr())
	err = forward(ctx, h, target.ID, ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// forward carries each local TCP connection over its own stream to the share.
func forward(ctx context.Context, h host.Host, target peer.ID, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			s, err := h.NewStream(ctx, target, protocolID)
			if err != nil {
				log.Printf("open stream: %v", err)
				c.Close()
				return
			}
			pipe(c, s)
		}()
	}
}

func pipe(c net.Conn, s network.Stream) {
	done := make(chan struct{})
	go func() {
		io.Copy(s, c)
		s.CloseWrite()
		close(done)
	}()
	io.Copy(c, s)
	c.Close()
	<-done
	s.Close()
}
