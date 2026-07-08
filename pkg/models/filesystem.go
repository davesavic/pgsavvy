package models

import "time"

type FSEntry struct {
	Name      string
	Path      string
	IsDir     bool
	SizeBytes int64
	ModTime   time.Time
}
