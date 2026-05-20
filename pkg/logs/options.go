package logs

import (
	"io"
	"time"

	"github.com/spf13/afero"
)

// Clock is duplicated locally to avoid importing pkg/common (which would risk
// import cycles later when common.LogCloser is added). It mirrors the time
// surface this package needs.
type Clock interface {
	Now() time.Time
}

// Options configures Open(). All fields are optional except Dir.
type Options struct {
	Dir            string   // logs root; "sessions/" subdir is created underneath
	FS             afero.Fs // typed as INTERFACE; never type-asserted
	Clock          Clock    // for filename generation; defaults to wallClock if nil
	RetentionCount int      // count of *.log files to keep (>=1; 20 is canonical)
	Redactor       Redactor // hook applied to every entry; may be nil (no-op)
	Categories     []string // if non-empty, only entries with matching `cat` field are written. Nil/empty = allow-all.
	BuildInfo      BuildInfo
	Pid            int       // optional override for tests; 0 → os.Getpid()
	Stderr         io.Writer // optional override for tests; nil → os.Stderr
}

// BuildInfo is defined here (NOT imported from pkg/app — would create cycle).
// pkg/app constructs an Options{BuildInfo: logs.BuildInfo{...}} at T3 wiring.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// wallClock is the default Clock implementation used when Options.Clock is nil.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
