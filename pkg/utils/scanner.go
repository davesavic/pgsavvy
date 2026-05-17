package utils

import (
	"bufio"
	"bytes"

	"github.com/davesavic/dbsavvy/pkg/constants"
)

// ScanLinesAndTruncateWhenLongerThanBuffer is a bufio.SplitFunc that behaves
// like bufio.ScanLines but silently truncates any single line longer than
// constants.MaxLineLength (1 MiB). It is intended for non-UI scan paths where
// dropping bytes is preferable to bufio.ErrTooLong; UI paths should render
// their own truncation marker (deferred to E5+).
func ScanLinesAndTruncateWhenLongerThanBuffer(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line := dropCR(data[0:i])
		if len(line) > constants.MaxLineLength {
			line = line[:constants.MaxLineLength]
		}
		return i + 1, line, nil
	}

	if atEOF {
		line := dropCR(data)
		if len(line) > constants.MaxLineLength {
			line = line[:constants.MaxLineLength]
		}
		return len(data), line, nil
	}

	if len(data) > constants.MaxLineLength {
		return constants.MaxLineLength, data[:constants.MaxLineLength], nil
	}

	return 0, nil, nil
}

var _ bufio.SplitFunc = ScanLinesAndTruncateWhenLongerThanBuffer

func dropCR(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\r' {
		return b[:len(b)-1]
	}
	return b
}
