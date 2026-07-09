package models

import "time"

type FSEntry struct {
	Name      string
	Path      string
	IsDir     bool
	IsSymlink bool
	SizeBytes int64
	ModTime   time.Time
}
