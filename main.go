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

//go:embed skills/hud/SKILL.md
var hudSkillBody []byte

func main() {
	if err := cmd.New(strings.TrimSpace(versionFile), hudSkillBody).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
