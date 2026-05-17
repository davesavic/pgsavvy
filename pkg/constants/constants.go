package constants

// AppName is the canonical short name used in paths, env vars, and log filenames.
const AppName = "dbsavvy"

// DefaultLogFile is the basename of the log file created under the XDG state dir.
const DefaultLogFile = "dbsavvy.log"

// MaxLineLength caps line length for scanners that must not blow memory on
// pathological input. 1 MiB. See pkg/utils.ScanLinesAndTruncateWhenLongerThanBuffer.
const MaxLineLength = 1 << 20

// XDGAppDir is the subdirectory name placed under each XDG base directory
// (XDG_CONFIG_HOME, XDG_STATE_HOME, XDG_CACHE_HOME).
const XDGAppDir = "dbsavvy"
