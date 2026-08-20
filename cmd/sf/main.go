// Command sf is a command-line interface for the SuperFaktura API.
package main

import (
	"runtime/debug"

	"github.com/xseman/superfaktura-cli/internal/commands"
)

// version is set by the release build; a `go install` build reads it back from
// the embedded module info instead.
var version = "dev"

func main() {
	if version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	commands.SetVersion(version)
	commands.Execute()
}
