// Command sign generates ed25519 key pairs and signs release assets for the
// rdev auto-updater. The public key is embedded in the updaters; release CI
// signs each asset so tampered downloads are rejected before self-update.
//
// Usage:
//
//	go run ./scripts/sign -generate          # print base64 public/private key
//	go run ./scripts/sign -key <b64priv> f…  # write <f>.sig (base64 signature)
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
)

func main() {
	generate := flag.Bool("generate", false, "generate a new ed25519 key pair and print it")
	key := flag.String("key", "", "base64-encoded ed25519 private key (64 bytes) used to sign files")
	flag.Parse()

	if *generate {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fatal("generate key: %v", err)
		}
		fmt.Printf("public:  %s\n", base64.StdEncoding.EncodeToString(pub))
		fmt.Printf("private: %s\n", base64.StdEncoding.EncodeToString(priv))
		return
	}

	if *key == "" || flag.NArg() == 0 {
		fatal("usage: sign -generate | sign -key <base64-private-key> file...")
	}
	priv, err := base64.StdEncoding.DecodeString(*key)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		fatal("invalid private key: must be base64 of %d bytes", ed25519.PrivateKeySize)
	}
	for _, path := range flag.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fatal("read %s: %v", path, err)
		}
		sig := ed25519.Sign(ed25519.PrivateKey(priv), data)
		out := path + ".sig"
		if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
			fatal("write %s: %v", out, err)
		}
		fmt.Printf("%s\n", out)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sign: "+format+"\n", args...)
	os.Exit(1)
}
