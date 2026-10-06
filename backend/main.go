// Command transfersh is the TransferCLI build of the transfer.sh upload server.
//
// It runs upstream transfer.sh (github.com/dutchcoders/transfer.sh) unchanged. This module exists to
// pin the upstream commit and the whole dependency graph (go.sum), so that every TransferCLI release
// ships a transfer.sh built from reviewed sources with a current Go toolchain. The last upstream
// release binary (v1.6.1, 2023) was built with Go 1.21 and is affected by more than 100 known
// vulnerabilities; see docs/security.md.
package main

import (
	"log"
	"os"

	"github.com/dutchcoders/transfer.sh/cmd"
)

func main() {
	app := cmd.New()
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
