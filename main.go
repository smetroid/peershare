// Command peershare shares a directory with specific peers, identified by
// public key, over an encrypted libp2p connection served as WebDAV.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// version is set at release build time.
var version = "dev"

const usage = `peershare - share a directory with peers you name by public key

Usage:
  peershare id                                   print your peer ID
  peershare share [flags] <dir>                  share dir, print a ticket
  peershare mount [flags] <ticket>               serve a share on localhost as WebDAV
  peershare relay [flags]                        run a relay for peers behind NAT
  peershare version                              print the version

Run "peershare <command> -h" for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "id":
		err = runID(args)
	case "share":
		err = runShare(ctx, args)
	case "mount":
		err = runMount(ctx, args)
	case "relay":
		err = runRelay(ctx, args)
	case "version", "-version", "--version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "peershare:", err)
		os.Exit(1)
	}
}

func runID(args []string) error {
	fs := flag.NewFlagSet("id", flag.ExitOnError)
	identity := fs.String("identity", defaultIdentityPath(), "private key file (created if missing)")
	fs.Parse(args)

	id, err := loadPeerID(*identity)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

// listFlag collects a repeatable or comma-separated string flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}
