package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// protocolID names the stream protocol carrying WebDAV between peers.
const protocolID = "/peershare/webdav/1.0.0"

var defaultListen = []string{
	"/ip4/0.0.0.0/tcp/0",
	"/ip4/0.0.0.0/udp/0/quic-v1",
	"/ip6/::/tcp/0",
	"/ip6/::/udp/0/quic-v1",
}

// newPeerHost builds a host for share or mount. With relays it reserves a
// slot on them so peers behind NAT can reach it, then upgrades to a direct
// connection by hole punching when the network allows.
func newPeerHost(sk crypto.PrivKey, listen []string, relays []peer.AddrInfo) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.Identity(sk),
		libp2p.ListenAddrStrings(listen...),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
	}
	if len(relays) > 0 {
		opts = append(opts,
			libp2p.EnableAutoRelayWithStaticRelays(relays),
			libp2p.ForceReachabilityPrivate(),
		)
	}
	return libp2p.New(opts...)
}

func parseRelays(addrs []string) ([]peer.AddrInfo, error) {
	var relays []peer.AddrInfo
	for _, a := range addrs {
		ai, err := peer.AddrInfoFromString(a)
		if err != nil {
			return nil, fmt.Errorf("relay %q: %w", a, err)
		}
		relays = append(relays, *ai)
	}
	return relays, nil
}

// connectRelays fails fast when no relay is reachable. Autorelay then
// reserves a slot on each and keeps the reservation refreshed.
func connectRelays(ctx context.Context, h host.Host, relays []peer.AddrInfo) error {
	var errs []error
	for _, r := range relays {
		if err := h.Connect(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", r.ID, err))
		}
	}
	if len(relays) > 0 && len(errs) == len(relays) {
		return errors.Join(errs...)
	}
	for _, err := range errs {
		log.Print(err)
	}
	return nil
}

// circuitAddrs are the share's addresses through each relay. They are built
// from the relay addresses given on the command line, because libp2p only
// advertises relay addresses it considers public.
func circuitAddrs(relays []peer.AddrInfo) []ma.Multiaddr {
	circuit := ma.StringCast("/p2p-circuit")
	var addrs []ma.Multiaddr
	for _, r := range relays {
		id := ma.StringCast("/p2p/" + r.ID.String())
		for _, a := range r.Addrs {
			addrs = append(addrs, a.Encapsulate(id).Encapsulate(circuit))
		}
	}
	return addrs
}

func parsePeerIDs(ids []string) (map[peer.ID]bool, error) {
	allowed := make(map[peer.ID]bool, len(ids))
	for _, s := range ids {
		id, err := peer.Decode(s)
		if err != nil {
			return nil, fmt.Errorf("peer ID %q: %w", s, err)
		}
		allowed[id] = true
	}
	return allowed, nil
}

// A ticket is a share's peer ID and addresses, encoded for copy and paste.
// It is not a secret: access is granted by the share's allowlist.
func encodeTicket(ai peer.AddrInfo) (string, error) {
	b, err := ai.MarshalJSON()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeTicket(s string) (peer.AddrInfo, error) {
	var ai peer.AddrInfo
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ai, fmt.Errorf("invalid ticket: %w", err)
	}
	if err := ai.UnmarshalJSON(b); err != nil {
		return ai, fmt.Errorf("invalid ticket: %w", err)
	}
	return ai, nil
}
