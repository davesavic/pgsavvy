package main

import (
	"log"
	"os"

	"github.com/davesavic/pgsavvy/pkg/app"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/session"
)

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
