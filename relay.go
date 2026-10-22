package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

func runRelay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	identity := fs.String("identity", defaultIdentityPath(), "private key file (created if missing)")
	var allow, listen listFlag
	fs.Var(&allow, "allow", "peer ID allowed to reserve a slot, i.e. a sharer (repeatable, required)")
	fs.Var(&listen, "listen", "listen multiaddr (default tcp and quic on port 4001)")
	fs.Parse(args)

	if len(allow) == 0 {
		return errors.New("at least one -allow <peer ID> is required")
	}
	allowed, err := parsePeerIDs(allow)
	if err != nil {
		return err
	}
	if len(listen) == 0 {
		listen = []string{"/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic-v1"}
	}
	sk, err := loadIdentity(*identity)
	if err != nil {
		return err
	}

	// Limits are off so a relayed connection still works when hole punching
	// fails; the ACL keeps that bandwidth for sharers you allowed.
	h, err := libp2p.New(
		libp2p.Identity(sk),
		libp2p.ListenAddrStrings(listen...),
		libp2p.ForceReachabilityPublic(),
		libp2p.EnableRelayService(relay.WithInfiniteLimits(), relay.WithACL(reserveACL(allowed))),
	)
	if err != nil {
		return err
	}
	defer h.Close()

	fmt.Println("Relay running. Use one of these with -relay (swap in your public address):")
	addrs, _ := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	for _, a := range addrs {
		fmt.Println(" ", a)
	}
	<-ctx.Done()
	return nil
}

// reserveACL lets only allowed sharers reserve a slot. Anyone may connect
// through a reservation; the sharer's own allowlist then decides access.
type reserveACL map[peer.ID]bool

func (a reserveACL) AllowReserve(p peer.ID, _ ma.Multiaddr) bool { return a[p] }

func (a reserveACL) AllowConnect(_ peer.ID, _ ma.Multiaddr, dest peer.ID) bool { return a[dest] }
