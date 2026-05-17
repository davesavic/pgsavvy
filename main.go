package main

import (
	"log"
	"os"

	"github.com/davesavic/dbsavvy/pkg/app"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/session"
)

var (
	commit      string
	date        string
	version     string
	buildSource string
)

func init() {
	drivers.Register("postgres", pg.New(session.TerminalPrompter{}))
}

func main() {
	buildInfo := &app.BuildInfo{
		Commit:      commit,
		Date:        date,
		Version:     version,
		BuildSource: buildSource,
	}
	if err := app.Start(buildInfo, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
