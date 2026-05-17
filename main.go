package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/meloniteai/sidekick/cmd"
)

//go:embed version
var versionFile string

//go:embed skills/sidekick/SKILL.md
var sidekickSkillBody []byte

func main() {
	if err := cmd.New(strings.TrimSpace(versionFile), sidekickSkillBody).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
