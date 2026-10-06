// Command webdump writes the web UI that is compiled into transfer.sh (github.com/dutchcoders/transfer.sh-web,
// the version pinned in ../../go.mod) to a directory, so scripts/build-release.sh can adjust it and the
// installer can serve it with WEB_PATH. Usage: go run ./cmd/webdump <output-dir>
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	web "github.com/dutchcoders/transfer.sh-web"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: webdump <output-dir>")
		os.Exit(2)
	}
	out := os.Args[1]
	for _, p := range web.AssetNames() {
		b, err := web.Asset(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, web.Prefix), "/")
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		dst := filepath.Join(out, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
