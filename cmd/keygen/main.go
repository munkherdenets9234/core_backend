// Command keygen prints a fresh Ed25519 signing keypair.
//
// The private half is tenantcore's TOKEN_PRIVATE_KEY. The public half goes
// into every product service that needs to verify a token.
//
// It exists as a command rather than a README instruction because the
// alternative is people generating keys with whatever one-liner they find,
// and the failure mode of a weak or wrongly-encoded signing key is silent:
// everything works until someone forges a superadmin token.
package main

import (
	"fmt"
	"os"

	"github.com/eandstravel/tenantcore/pkg/token"
)

func main() {
	priv, pub, err := token.GenerateKeyPair()
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}

	fmt.Println("# tenantcore signing keypair")
	fmt.Println("#")
	fmt.Println("# Put the private key in tenantcore's own environment. It is the most")
	fmt.Println("# sensitive value in the platform: whoever holds it can mint a")
	fmt.Println("# superadmin token that every product service will accept.")
	fmt.Println()
	fmt.Printf("TOKEN_PRIVATE_KEY=%s\n", priv)
	fmt.Println()
	fmt.Println("# Put the public key in every product service. It can only verify.")
	fmt.Println("# Services can also fetch it from GET /.well-known/tenantcore.")
	fmt.Println()
	fmt.Printf("TOKEN_PUBLIC_KEY=%s\n", pub)
}
