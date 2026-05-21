package logs

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// BenchmarkRedactingHandler_AllocsPerOp measures allocations per emission
// through the redactingHandler wrapped around a RecordingHandler.
func BenchmarkRedactingHandler_AllocsPerOp(b *testing.B) {
	h := &redactingHandler{next: NewRecordingHandler(), redactor: DefaultRedactor()}
	conn := models.Connection{
		Name:     "primary",
		Driver:   "postgres",
		DSN:      "postgres://u:s3cret@h/d",
		Password: "hunter2",
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelDebug, "opening postgres://u:s3cret@h/d", 0)
		r.AddAttrs(slog.Any("conn", conn))
		_ = h.Handle(ctx, r)
	}
}
