package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/uriahlevy/hud/cmd"
)

//go:embed version
var versionFile string

func main() {
	if err := cmd.New(strings.TrimSpace(versionFile)).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
