#!/usr/bin/env bash
# End-to-end test: two peers behind separate NATs, connected through a relay
# and, with --cloudflare, through a Cloudflare quick tunnel. See compose.yaml
# for the topology. Needs Docker; --cloudflare also needs internet access.
#
#   e2e/run.sh                 relay checks
#   e2e/run.sh --reconnect     also check a mount survives a dropped connection
#                              (adds about 2.5 minutes)
#   e2e/run.sh --cloudflare    relay checks, then the same through Cloudflare
#   KEEP=1 e2e/run.sh          leave the containers running for debugging
set -euo pipefail
cd "$(dirname "$0")"

cloudflare=false
reconnect=false
for arg in "$@"; do
	case $arg in
	--cloudflare) cloudflare=true ;;
	--reconnect) reconnect=true ;;
	*) echo "usage: $0 [--reconnect] [--cloudflare]" >&2; exit 2 ;;
	esac
done

dc() { docker compose --progress quiet -p peershare-e2e "$@"; }
x() { dc exec -T "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

cleanup() {
	status=$?
	if ((status != 0)); then
		for s in relay share mount; do
			echo "--- $s logs" >&2
			x "$s" sh -c 'cat /tmp/*.log 2>/dev/null' >&2 || true
		done
	fi
	[[ ${KEEP:-} == 1 ]] || dc --profile cloudflare down -v -t 1 >/dev/null 2>&1
}
trap cleanup EXIT

# wait_for <service> <file> <pattern>: print the first match once it shows up.
wait_for() {
	for _ in $(seq 60); do
		if m=$(x "$1" grep -Eo -m1 "$3" "$2" 2>/dev/null); then
			echo "$m"
			return
		fi
		sleep 1
	done
	fail "$1: no '$3' in $2"
}

# start <service> <log> <args...>: run peershare in the background.
start() {
	local svc=$1 log=$2
	shift 2
	dc exec -d "$svc" sh -c 'exec peershare "$@" >'"$log"' 2>&1' sh "$@"
}

# mount_ticket <ticket> <identity> <port>: mount in the mount container.
mount_ticket() {
	start mount "/tmp/mount-$3.log" mount -identity "$2" -addr "127.0.0.1:$3" "$1"
}

get() { x mount curl -s --max-time 30 "$@"; }

echo "Building and starting the containers"
dc build -q
dc up -d --wait

SHARE_ID=$(x share peershare id -identity /tmp/share.key)
FRIEND_ID=$(x mount peershare id -identity /tmp/friend.key)
RELAY_ID=$(x relay peershare id -identity /tmp/relay.key)
x share sh -c 'mkdir -p /srv/dir && echo hello > /srv/dir/a.txt'

# The share listens on a fixed port so we can check that the mount side
# can't reach it directly. Otherwise a pass would prove nothing about NAT.
start relay /tmp/relay.log relay -identity /tmp/relay.key -allow "$SHARE_ID" -listen /ip4/0.0.0.0/tcp/4001
wait_for relay /tmp/relay.log 'Relay running' >/dev/null
start share /tmp/share.log share -identity /tmp/share.key -allow "$FRIEND_ID" \
	-listen /ip4/0.0.0.0/tcp/4001 -relay "/ip4/10.99.0.10/tcp/4001/p2p/$RELAY_ID" /srv/dir
TICKET=$(wait_for share /tmp/share.log 'peershare mount [^ ]+' | cut -d' ' -f3)

# curl exits 7 (refused/unreachable) or 28 (timeout) when it can't connect.
rc=0
x mount curl -s -o /dev/null --connect-timeout 3 http://10.99.1.10:4001/ || rc=$?
[[ $rc == 7 || $rc == 28 ]] || fail "mount can reach the share directly; the NAT isn't isolating the LANs"
pass "mount can't reach the share directly"

mount_ticket "$TICKET" /tmp/friend.key 8484
wait_for mount /tmp/mount-8484.log 'Connected to' >/dev/null
[[ $(get http://127.0.0.1:8484/a.txt) == hello ]] || fail "read through the relay"
pass "allowed peer reads the share through the relay"

code=$(get -o /dev/null -w '%{http_code}' -T /etc/hosts http://127.0.0.1:8484/x)
[[ $code == 403 ]] || fail "write to a read-only share returned $code, want 403"
pass "read-only share refuses writes"

mount_ticket "$TICKET" /tmp/stranger.key 8485
wait_for mount /tmp/mount-8485.log 'Connected to' >/dev/null
if [[ $(get http://127.0.0.1:8485/a.txt || true) == hello ]]; then
	fail "a peer not on the allowlist read the share"
fi
wait_for share /tmp/share.log 'rejected peer' >/dev/null
pass "peer not on the allowlist is rejected"

if $reconnect; then
	# Restart the share so the mount's connection drops, then wait out the
	# 2 minute TTL libp2p gives addresses it has no live connection for. The
	# mount must still reach the share from the ticket's addresses.
	x share pkill peershare
	start share /tmp/share-2.log share -identity /tmp/share.key -allow "$FRIEND_ID" \
		-listen /ip4/0.0.0.0/tcp/4001 -relay "/ip4/10.99.0.10/tcp/4001/p2p/$RELAY_ID" /srv/dir
	wait_for share /tmp/share-2.log 'peershare mount [^ ]+' >/dev/null
	echo "Waiting for the mount's address entries to expire"
	sleep 135
	got=
	for _ in $(seq 30); do # the share needs a moment to re-reserve its relay slot
		got=$(get http://127.0.0.1:8484/a.txt || true)
		[[ $got == hello ]] && break
		sleep 2
	done
	[[ $got == hello ]] || fail "read after the connection dropped and addresses expired"
	pass "mount reconnects from the ticket after its connection drops"
fi

$cloudflare || exit 0

# Restart the share on a loopback WebSocket listener only, so the tunnel is
# the one way in, and leave out the relay.
x share pkill peershare
dc --profile cloudflare up -d cloudflared
for _ in $(seq 60); do
	HOST=$(dc logs cloudflared 2>&1 | grep -Eo -m1 '[a-z0-9-]+\.trycloudflare\.com' || true)
	[[ -n $HOST ]] && break
	sleep 1
done
[[ -n $HOST ]] || fail "cloudflared printed no trycloudflare.com host"

start share /tmp/share-cf.log share -identity /tmp/share.key -allow "$FRIEND_ID" \
	-listen /ip4/127.0.0.1/tcp/4002/ws -announce "/dns4/$HOST/tcp/443/wss" /srv/dir
TICKET=$(wait_for share /tmp/share-cf.log 'peershare mount [^ ]+' | cut -d' ' -f3)

# A new quick tunnel takes a while to resolve and answer. Cloudflare returns
# 5xx until it reaches the share, which then answers the plain GET itself.
for _ in $(seq 60); do
	code=$(get -o /dev/null -w '%{http_code}' "https://$HOST/" || true)
	[[ $code =~ ^[1-4] ]] && break
	sleep 2
done
mount_ticket "$TICKET" /tmp/friend.key 8486
wait_for mount /tmp/mount-8486.log 'Connected to' >/dev/null
[[ $(get http://127.0.0.1:8486/a.txt) == hello ]] || fail "read through Cloudflare"
pass "allowed peer reads the share through Cloudflare Tunnel"
