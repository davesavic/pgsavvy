package main

import (
	_ "embed"
	"log"
	"os"

	"github.com/davesavic/pgsavvy/pkg/app"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/session"
)

//go:embed RELEASE_NOTES.txt
var releaseNotes []byte

var (
	commit      string
	date        string
	version     string
	buildSource string
)

func init() {
	drivers.Register("postgres", pg.New(session.TUIRefusePrompter{}))
}

func main() {
	if handled, exit := runMachine(os.Args[1:]); handled {
		os.Exit(exit)
	}
	buildInfo := &app.BuildInfo{
		Commit:      commit,
		Date:        date,
		Version:     version,
		BuildSource: buildSource,
	}
	if err := app.Start(buildInfo, string(releaseNotes), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
