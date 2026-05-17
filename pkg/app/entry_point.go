package app

import "fmt"

// BuildInfo carries build-time metadata injected via -ldflags.
type BuildInfo struct {
	Commit      string
	Date        string
	Version     string
	BuildSource string
}

// Start is the CLI entry point. Downstream epics (dbsavvy-8pa) replace this stub body.
func Start(build *BuildInfo, args []string) error {
	fmt.Printf("dbsavvy %s (%s)\n", build.Version, build.BuildSource)
	return nil
}
