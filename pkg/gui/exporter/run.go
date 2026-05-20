package exporter

import (
	"context"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// RowSource produces rows for a single export run.
// Iterate calls fn for each row until fn returns an error or the source is
// exhausted. Implementations are scope-aware (Visible / Loaded / Full).
type RowSource interface {
	Cols() []models.ColumnMeta
	Iterate(fn func(models.Row) error) error
}

// ProgressFn is called with the current row count at most every ~5000 rows
// or 1s, whichever comes first. Optional (may be nil).
type ProgressFn func(rowsWritten int64)

// Run drives one Format → Destination export over a RowSource.
// Cancellation: ctx.Done() interrupts the row loop AT the next row boundary.
//   - file dest: .partial is removed (Abort).
//   - clipboard dest: buffer is discarded.
//   - stdout dest: write loop returns; os.Stdout remains open.
//
// Returns the human-readable destination descriptor (filename, "clipboard",
// "stdout") on success, or an error.
func Run(ctx context.Context, format Format, dest Destination, src RowSource, progress ProgressFn) (string, error) {
	wc, descriptor, err := dest.Open()
	if err != nil {
		return "", err
	}
	// Track success so deferred cleanup can decide rename vs abort.
	success := false
	defer func() {
		if !success {
			// Best-effort cleanup. fileDest knows how to remove .partial; for
			// others Close is a no-op or has already been called.
			if fd, ok := dest.(*fileDest); ok {
				fd.Abort()
			} else {
				_ = wc.Close()
			}
		}
	}()

	cols := src.Cols()
	if err := format.Header(cols, wc); err != nil {
		return "", err
	}

	var rowCount int64
	var lastProgressAt time.Time
	const progressInterval = time.Second
	const progressEveryN = 5000

	err = src.Iterate(func(r models.Row) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := format.Row(r, wc); err != nil {
			return err
		}
		rowCount++
		if progress != nil {
			if rowCount%progressEveryN == 0 || time.Since(lastProgressAt) >= progressInterval {
				progress(rowCount)
				lastProgressAt = time.Now()
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := format.Footer(wc); err != nil {
		return "", err
	}
	if err := wc.Close(); err != nil {
		return "", err
	}
	success = true
	if progress != nil {
		progress(rowCount) // final tick
	}
	return descriptor, nil
}
