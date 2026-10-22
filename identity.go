package main

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func defaultIdentityPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "peershare", "identity.key")
}

// loadIdentity reads the Ed25519 key at path, creating it on first use.
// The peer ID other people allow is derived from this key.
func loadIdentity(path string) (crypto.PrivKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return crypto.UnmarshalPrivateKey(b)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	sk, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	if b, err = crypto.MarshalPrivateKey(sk); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	return sk, f.Close()
}

func loadPeerID(path string) (peer.ID, error) {
	sk, err := loadIdentity(path)
	if err != nil {
		return "", err
	}
	return peer.IDFromPrivateKey(sk)
}
