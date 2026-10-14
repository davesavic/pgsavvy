// Command releasenotes blocks release publication when the tag has no notes.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: releasenotes <release-tag>")
		os.Exit(1)
	}
	notes, err := os.ReadFile("RELEASE_NOTES.txt")
	if err == nil {
		err = validateReleaseNotes(string(notes), os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "release notes validation failed: %v\n", err)
		os.Exit(1)
	}
}

func validateReleaseNotes(notes, tag string) error {
	heading := "pgsavvy " + tag
	found := false
	for line := range strings.SplitSeq(notes, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !found {
			found = line == heading
			continue
		}
		if strings.HasPrefix(line, "pgsavvy ") {
			break
		}
		if strings.TrimSpace(line) != "" {
			return nil
		}
	}
	if !found {
		return fmt.Errorf("RELEASE_NOTES.txt must contain the exact heading %q before tagging a release", heading)
	}
	return fmt.Errorf("RELEASE_NOTES.txt entry for %q is empty; add release notes before tagging", tag)
}
