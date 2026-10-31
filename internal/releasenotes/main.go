// Command releasenotes blocks release publication when the tag has no notes.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	printNotes := len(args) > 0 && args[0] == "--print"
	if printNotes {
		args = args[1:]
	}
	if len(args) != 1 || args[0] == "" {
		_, _ = fmt.Fprintln(stderr, "usage: releasenotes [--print] <release-tag>")
		return 1
	}
	notes, err := os.ReadFile("RELEASE_NOTES.txt")
	var section string
	if err == nil {
		section, err = releaseNotesSection(string(notes), args[0])
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "release notes validation failed: %v\n", err)
		return 1
	}
	if printNotes {
		if written, err := io.WriteString(stdout, section); err != nil || written != len(section) {
			if err == nil {
				err = io.ErrShortWrite
			}
			_, _ = fmt.Fprintf(stderr, "release notes output failed: %v\n", err)
			return 1
		}
	}
	return 0
}

func validateReleaseNotes(notes, tag string) error {
	_, err := releaseNotesSection(notes, tag)
	return err
}

// releaseNotesSection normalizes CRLF and ends the selected section with one newline.
func releaseNotesSection(notes, tag string) (string, error) {
	heading := "pgsavvy " + tag
	found, hasNotes := false, false
	var section strings.Builder
	for line := range strings.SplitSeq(notes, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !found {
			found = line == heading
			if found {
				section.WriteString(line + "\n")
			}
			continue
		}
		if strings.HasPrefix(line, "pgsavvy ") {
			break
		}
		if strings.TrimSpace(line) != "" {
			hasNotes = true
		}
		section.WriteString(line + "\n")
	}
	if !found {
		return "", fmt.Errorf("RELEASE_NOTES.txt must contain the exact heading %q before tagging a release", heading)
	}
	if !hasNotes {
		return "", fmt.Errorf("RELEASE_NOTES.txt entry for %q is empty; add release notes before tagging", tag)
	}
	return strings.TrimRight(section.String(), "\n") + "\n", nil
}
