# peershare

Share a directory with specific people, identified by public key. There are
no accounts and no central server. Each machine has an Ed25519 key, and the
share only serves peers whose key is on its allowlist. The connection is
end-to-end encrypted (libp2p Noise/TLS). On the other side it shows up as a
WebDAV server on localhost, which macOS, Windows and Linux can all mount
without extra drivers.

## Install

```sh
brew install --cask smetroid/tap/peershare
```

Or download a binary for macOS, Linux or Windows from
[Releases](https://github.com/smetroid/peershare/releases), or build it
with `go build -o peershare .`. The macOS binary isn't notarized. The cask
clears its quarantine flag. If you download it by hand, run
`xattr -d com.apple.quarantine peershare`.

## Use

```sh

# Co-worker: print the peer ID and send it to you (it's not a secret)
peershare id

# You: share a folder with that peer, read-only by default (-rw to allow writes)
peershare share -allow 12D3KooW... ~/Shared/project
# prints: peershare mount <ticket>

# Co-worker: run the printed command, then mount the local URL
peershare mount <ticket>
# Finder: Go > Connect to Server (Cmd-K) > http://127.0.0.1:8484
```

The key lives in `~/Library/Application Support/peershare/identity.key`
(`os.UserConfigDir`). Use `-identity` to choose another file.

## Across NATs

Two machines on different home or office networks usually can't dial each
other directly. Run a relay somewhere both can reach, such as a public host,
your cluster, or a port forward on OPNsense:

```sh
peershare relay -allow <your peer ID>      # listens on tcp+udp 4001
peershare share -allow <theirs> -relay /ip4/<public-ip>/tcp/4001/p2p/<relay ID> ~/dir
```

The ticket then includes the relay route. libp2p attempts hole punching
(DCUtR) to upgrade to a direct connection and keeps using the relay when that
fails. The relay has no bandwidth limits, so its ACL only lets peers you
`-allow` reserve slots on it, and only those reserved peers can be reached
through it.

## Through Cloudflare Tunnel

If you don't want to open a port, put the share behind a
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/).
The share listens for libp2p over WebSocket on localhost, `cloudflared` publishes
it as an HTTPS hostname, and peers dial it over `wss` on port 443. Cloudflare
terminates TLS but only sees libp2p's encrypted stream. You don't need a relay.

```
co-worker ──wss:443──▶ Cloudflare ◀── cloudflared (outbound) ──▶ 127.0.0.1:4002/ws ── peershare share
```

One-time setup with a domain on Cloudflare:

```sh
brew install cloudflared
cloudflared tunnel login                                  # pick the zone
cloudflared tunnel create peershare
cloudflared tunnel route dns peershare share.example.com  # creates the CNAME
```

Each time you share, run the tunnel and the share:

```sh
cloudflared tunnel run --url http://localhost:4002 peershare

peershare share -allow <their ID> \
  -listen /ip4/127.0.0.1/tcp/4002/ws \
  -announce /dns4/share.example.com/tcp/443/wss \
  ~/dir
```

`-announce` adds the public address to the ticket, because the share can't
discover it itself. The co-worker runs `peershare mount <ticket>` as usual and
needs no Cloudflare account or `cloudflared`.

For a one-off without a domain, `cloudflared tunnel --url http://localhost:4002`
prints a random `https://<name>.trycloudflare.com` hostname. Announce it as
`/dns4/<name>.trycloudflare.com/tcp/443/wss`. The hostname changes every run,
so you have to send a new ticket each time.

Notes:

- Anyone can open a WebSocket to the hostname. The share then rejects any peer
  that's not on `-allow`, the same as on any other address.
- Don't put Cloudflare Access in front of the hostname. The `peershare` client
  can't complete its login, so the connection would fail.
- All traffic goes through Cloudflare, which is slower than a direct
  connection for large transfers. Listening only on `127.0.0.1` also turns off
  LAN and direct connections. Add the default listen addresses with more
  `-listen` flags if you want those as well.

## Security model

- Access is granted per peer ID on the share side. libp2p proves the remote
  key during the handshake. The ticket is not a secret: it's an address.
- Paths that resolve outside the shared directory through symlinks are refused.
- `mount` binds to 127.0.0.1, so other local users on the same machine can
  reach the mounted share too.

## Testing

### Automated

```sh
go test -race ./...
```

The tests run several peers in one process on loopback. They check that:

- an allowed peer can read the share
- a peer not on the allowlist is refused
- writes to a read-only share are rejected
- symlinks pointing out of the share are refused
- a ticket with only relay addresses still connects
- a share listening on `/ws` is reachable through that address alone, as
  it is behind Cloudflare Tunnel

CI runs the tests on every push and pull request. The `security` workflow
also runs on every push and pull request, and weekly:

- **govulncheck** reports known vulnerabilities in the Go toolchain and
  dependencies that the code actually reaches.
- **gitleaks** scans the full git history for committed secrets.
- **CodeQL** (`security-extended` queries) does static analysis. Its results
  appear under Security → Code scanning.

Dependabot opens weekly update PRs for Go modules and Actions. Go module
updates use `fix:` commits, so they cut a patch release with the fix. Run the
same checks locally:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
gitleaks git .
```

### By hand, on one machine

Use two identity files to act as two people:

```sh
mkdir -p /tmp/ps/dir && echo hello > /tmp/ps/dir/a.txt
FRIEND=$(peershare id -identity /tmp/ps/friend.key)

# terminal 1: the sharer
peershare share -identity /tmp/ps/me.key -allow $FRIEND /tmp/ps/dir

# terminal 2: the friend, pasting the ticket from terminal 1
peershare mount -identity /tmp/ps/friend.key <ticket>

# terminal 3
curl http://127.0.0.1:8484/a.txt                                       # hello
curl -s -o /dev/null -w '%{http_code}\n' -T /etc/hosts http://127.0.0.1:8484/x   # 403 (read-only)
```

Then mount `http://127.0.0.1:8484` in Finder (Cmd-K) and browse it. To check
that the allowlist works, run `mount` with a third `-identity` file. The
connection succeeds, but every request fails and `share` logs
`rejected peer`.

### With a co-worker on another network

1. Run the relay on a host with a public address, or port-forward 4001
   (TCP and UDP) to it on OPNsense:
   `peershare relay -allow $(peershare id)`
2. Share with their peer ID and the relay's address, using your public IP or
   DDNS name: `peershare share -allow <their ID> -relay /ip4/<public-ip>/tcp/4001/p2p/<relay ID> ~/dir`
3. They run `peershare mount <ticket>` and mount the URL in Finder.
4. Expected: the mount works over the relay. Running `share` with
   `GOLOG_LOG_LEVEL=p2p-holepunch=debug` shows whether hole punching upgraded
   the connection to a direct one.

### Through Cloudflare Tunnel, on one machine

Uses a quick tunnel, so no domain is needed:

```sh
cloudflared tunnel --url http://localhost:4002        # note the trycloudflare.com host
peershare share -identity /tmp/ps/me.key -allow $FRIEND \
  -listen /ip4/127.0.0.1/tcp/4002/ws -announce /dns4/<host>/tcp/443/wss /tmp/ps/dir
```

The ticket also includes the local `/ws` address, so mounting it on the same
machine would skip the tunnel. To be sure the tunnel works, have someone on
another network mount it.

### The Homebrew install

After a release, on a clean Mac or after `brew uninstall --cask peershare`:

```sh
brew install --cask smetroid/tap/peershare
peershare version          # matches the release tag, and runs without a Gatekeeper prompt
```

## Releasing

Releases are automated from [conventional commits](https://www.conventionalcommits.org/).

1. Merge to `main` with `feat:` or `fix:` commits. A `feat!:` or `BREAKING CHANGE`
   bumps the minor version while still below 1.0. `chore:`, `docs:` and `ci:`
   commits don't cut a release on their own.
2. release-please opens or updates a **release PR** with the next version and
   `CHANGELOG.md`.
3. Merging the release PR tags `vX.Y.Z` and creates the GitHub release.
   GoReleaser then attaches the binaries and checksums and pushes the cask to
   [smetroid/homebrew-tap](https://github.com/smetroid/homebrew-tap).

To test the GoReleaser config locally without publishing:
`HOMEBREW_TAP_DEPLOY_KEY=x goreleaser release --snapshot --clean`. The output
goes to `dist/`.

## Known gaps

- No revocation while running: restart `share` without the peer.
- The symlink check runs before each operation, not atomically with it.
- Read-only shares refuse all writes, including the `._*` and `.DS_Store`
  files Finder tries to create. Finder still browses and copies normally.
