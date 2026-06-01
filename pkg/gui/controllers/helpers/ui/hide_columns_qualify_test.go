package ui

import (
	"reflect"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestQualifyHideColumnNames(t *testing.T) {
	tests := []struct {
		name     string
		cols     []models.ColumnMeta
		oidNames map[uint32]string
		want     []string
	}{
		{
			name: "single table returns bare names even with resolved names",
			cols: []models.ColumnMeta{
				{Name: "id", TableOID: 100},
				{Name: "email", TableOID: 100},
			},
			oidNames: map[uint32]string{100: "users"},
			want:     []string{"id", "email"},
		},
		{
			name: "multi table qualifies every column",
			cols: []models.ColumnMeta{
				{Name: "id", TableOID: 100},
				{Name: "title", TableOID: 100},
				{Name: "id", TableOID: 200},
				{Name: "post_count", TableOID: 200},
			},
			oidNames: map[uint32]string{100: "posts", 200: "posts_summary"},
			want: []string{
				"posts.id",
				"posts.title",
				"posts_summary.id",
				"posts_summary.post_count",
			},
		},
		{
			name: "multi table with nil name map falls back to bare names",
			cols: []models.ColumnMeta{
				{Name: "id", TableOID: 100},
				{Name: "id", TableOID: 200},
			},
			oidNames: nil,
			want:     []string{"id", "id"},
		},
		{
			name: "computed column with zero oid stays bare while others qualify",
			cols: []models.ColumnMeta{
				{Name: "id", TableOID: 100},
				{Name: "email", TableOID: 200},
				{Name: "total", TableOID: 0},
			},
			oidNames: map[uint32]string{100: "posts", 200: "users"},
			want:     []string{"posts.id", "users.email", "total"},
		},
		{
			name: "multi table with unresolved oid falls back to bare for that column",
			cols: []models.ColumnMeta{
				{Name: "id", TableOID: 100},
				{Name: "email", TableOID: 200},
			},
			oidNames: map[uint32]string{100: "posts"},
			want:     []string{"posts.id", "email"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := qualifyHideColumnNames(tc.cols, tc.oidNames)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("qualifyHideColumnNames() = %v, want %v", got, tc.want)
			}
		})
	}
}
