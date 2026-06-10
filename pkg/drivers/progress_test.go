package drivers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
)

// recordingReporter captures the stages reported to it, in order.
type recordingReporter struct {
	stages []drivers.ConnectStage
}

func (r *recordingReporter) Report(stage drivers.ConnectStage) {
	r.stages = append(r.stages, stage)
}

// emitConnectSequence mirrors the conditional-emit logic in pg Open: it reports
// StageTunnel only when a tunnel was established, and StageAuthenticated only
// when the ping succeeded, in that order. It exists so the ordering and
// conditionality are unit-testable without a live database or bastion.
func emitConnectSequence(reporter drivers.ProgressReporter, tunnelEstablished, pingOK bool) {
	if tunnelEstablished {
		drivers.ReportStage(reporter, drivers.StageTunnel)
	}
	if pingOK {
		drivers.ReportStage(reporter, drivers.StageAuthenticated)
	}
}

func TestProgressReportStageNilReporterNoPanic(t *testing.T) {
	// A nil reporter must be a safe no-op at every emit site.
	drivers.ReportStage(nil, drivers.StageTunnel)
	drivers.ReportStage(nil, drivers.StageAuthenticated)
	emitConnectSequence(nil, true, true)
}

func TestProgressConditionalEmit(t *testing.T) {
	tests := []struct {
		name              string
		tunnelEstablished bool
		pingOK            bool
		want              []drivers.ConnectStage
	}{
		{
			name:              "tunnel and auth in order",
			tunnelEstablished: true,
			pingOK:            true,
			want:              []drivers.ConnectStage{drivers.StageTunnel, drivers.StageAuthenticated},
		},
		{
			name:              "no tunnel emits only auth",
			tunnelEstablished: false,
			pingOK:            true,
			want:              []drivers.ConnectStage{drivers.StageAuthenticated},
		},
		{
			name:              "ping failure emits no auth",
			tunnelEstablished: true,
			pingOK:            false,
			want:              []drivers.ConnectStage{drivers.StageTunnel},
		},
		{
			name:              "nothing established emits nothing",
			tunnelEstablished: false,
			pingOK:            false,
			want:              nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingReporter{}
			emitConnectSequence(rec, tt.tunnelEstablished, tt.pingOK)

			if len(rec.stages) != len(tt.want) {
				t.Fatalf("stages = %v, want %v", rec.stages, tt.want)
			}
			for i := range tt.want {
				if rec.stages[i] != tt.want[i] {
					t.Fatalf("stage[%d] = %v, want %v (full: %v)", i, rec.stages[i], tt.want[i], rec.stages)
				}
			}
		})
	}
}

func TestProgressConnectStageString(t *testing.T) {
	cases := map[drivers.ConnectStage]string{
		drivers.StageTunnel:        "tunnel",
		drivers.StageAuthenticated: "authenticated",
		drivers.ConnectStage(99):   "unknown",
	}
	for stage, want := range cases {
		if got := stage.String(); got != want {
			t.Errorf("ConnectStage(%d).String() = %q, want %q", stage, got, want)
		}
	}
}
