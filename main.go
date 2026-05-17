package main

import (
	"log"
	"os"

	"github.com/davesavic/dbsavvy/pkg/app"
)

var (
	commit      string
	date        string
	version     string
	buildSource string
)

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
