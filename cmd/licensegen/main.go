// Command licensegen manages on-premise license keys for dotsync.
//
// This is NOT part of the deployed server — it never runs in production
// and is never invoked by anything except you, by hand, on your own
// machine. It's safe for this whole tool to live in the public repo
// because it needs the private key as *input*; it never stores or embeds
// one anywhere.
//
// First-time setup (run once, ever):
//
//	go run ./cmd/licensegen genkeypair
//
// This prints a keypair. Save the private key somewhere safe — a password
// manager, a secrets vault, anything except this git repo — and paste the
// public key into internal/license.PublicKeyHex, then commit that one
// line. That's what every deployed server checks license keys against.
//
// Issuing a license after someone pays for the on-premise plan:
//
//	DOTSYNC_LICENSE_PRIVATE_KEY=<hex from genkeypair> \
//	  go run ./cmd/licensegen issue --licensee "Acme Inc"
//
// Email the printed key to the buyer. They set it as DOTSYNC_LICENSE_KEY
// on their own deployment.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/Pruthviraj36/dotsync/internal/license"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "genkeypair":
		genKeypair()
	case "issue":
		issue(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func genKeypair() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Println("Generated a new keypair. Save these NOW — this is the only time")
	fmt.Println("the private key is shown.")
	fmt.Println()
	fmt.Println("PUBLIC KEY  — safe to publish, paste into internal/license.PublicKeyHex:")
	fmt.Println(" ", hex.EncodeToString(pub))
	fmt.Println()
	fmt.Println("PRIVATE KEY — secret. Store in a password manager or secrets vault.")
	fmt.Println("              Never commit this to git, never post it anywhere.")
	fmt.Println(" ", hex.EncodeToString(priv))
}

func issue(args []string) {
	fs := flag.NewFlagSet("issue", flag.ExitOnError)
	licensee := fs.String("licensee", "", "name of the person/company this license is for")
	fs.Parse(args)

	if *licensee == "" {
		fmt.Fprintln(os.Stderr, "error: --licensee is required, e.g. --licensee \"Acme Inc\"")
		os.Exit(1)
	}

	privHex := os.Getenv("DOTSYNC_LICENSE_PRIVATE_KEY")
	if privHex == "" {
		fmt.Fprintln(os.Stderr, "error: set DOTSYNC_LICENSE_PRIVATE_KEY to the private key from 'genkeypair'")
		os.Exit(1)
	}
	privBytes, err := hex.DecodeString(privHex)
	if err != nil || len(privBytes) != ed25519.PrivateKeySize {
		fmt.Fprintln(os.Stderr, "error: DOTSYNC_LICENSE_PRIVATE_KEY isn't a valid ed25519 private key")
		os.Exit(1)
	}

	key, err := license.Issue(ed25519.PrivateKey(privBytes), *licensee)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Println("License key for", *licensee+":")
	fmt.Println()
	fmt.Println(key)
	fmt.Println()
	fmt.Println("Send this to the buyer. They set it as DOTSYNC_LICENSE_KEY on their deployment.")
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  go run ./cmd/licensegen genkeypair
  go run ./cmd/licensegen issue --licensee "Acme Inc"`)
}
