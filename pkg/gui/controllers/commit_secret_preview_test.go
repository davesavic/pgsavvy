package controllers

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestCommitPreviewEveryCredentialPath(t *testing.T) {
	const secret = "SYNTHETIC_PREVIEW_SECRET"
	const escaped = `\u0053YNTHETIC_PREVIEW_SECRET`
	for _, external := range []bool{false, true} {
		for _, mode := range []guicontext.CommitDialogMode{guicontext.CommitDialogPreview, guicontext.CommitDialogSqlPreview, guicontext.CommitDialogDryRunResult} {
			set := &models.PendingEditSet{Table: models.Ref{Schema: "app", Table: "items"}}
			if err := set.Add(models.PendingEdit{PrimaryKey: []any{secret}, Column: "value", ColumnType: "jsonb", OldValue: json.RawMessage(`{"nested":[{"value":"` + escaped + `"}]}`), NewValue: `{"nested":[{"value":"` + escaped + `"}]}`, NewExpr: "upper('" + secret + "')", Kind: models.Expression}); err != nil {
				t.Fatal(err)
			}
			before := set.Edits()
			profile := &models.Connection{Name: "fixture", Password: secret}
			view := guicontext.CommitDialogView{Set: set, Conn: profile, Mode: mode, PkCols: []string{"id"}, DryRunResult: []guicontext.DryRunStmtResult{{SQL: "UPDATE items SET value='" + secret + "' WHERE id='" + secret + "'"}}}
			if external {
				profile.Password = ""
				view.RedactSecrets = func(text string) string { return strings.ReplaceAll(text, secret, "***") }
			}
			body := DefaultCommitDialogRender(view)
			if strings.Contains(body, secret) || strings.Contains(body, escaped) || !strings.Contains(body, "***") {
				t.Fatal("value, nested escaped JSON, key, expression or dry-run preview exposed its synthetic credential")
			}
			if !reflect.DeepEqual(set.Edits(), before) || view.DryRunResult[0].SQL != "UPDATE items SET value='"+secret+"' WHERE id='"+secret+"'" {
				t.Fatal("preview redaction mutated stored edits or executable SQL")
			}
		}
	}
}

func TestCommitPreviewRedactsBinaryIdentityBeforeEncoding(t *testing.T) {
	const secret = "BINARY_PREVIEW_SECRET"
	set := &models.PendingEditSet{Table: models.Ref{Schema: "app", Table: "items"}}
	if err := set.Add(models.PendingEdit{PrimaryKey: []any{[]byte(secret)}, Column: "value", OldValue: "old", NewValue: "new"}); err != nil {
		t.Fatal(err)
	}
	before := set.Edits()
	body := DefaultCommitDialogRender(guicontext.CommitDialogView{Set: set, Conn: &models.Connection{Password: secret}, Mode: guicontext.CommitDialogSqlPreview, PkCols: []string{"id"}})
	if strings.Contains(body, secret) || strings.Contains(body, hex.EncodeToString([]byte(secret))) || !strings.Contains(body, hex.EncodeToString([]byte("***"))) || !reflect.DeepEqual(set.Edits(), before) {
		t.Fatal("bytea identity encoded a recoverable credential or changed the stored tuple")
	}
	batch, err := BuildCommitPreviewBatch(set.Table, []string{"id"}, set.Edits(), func(text string) string { return strings.ReplaceAll(text, secret, "***") })
	if err != nil {
		t.Fatal(err)
	}
	body = DefaultCommitDialogRender(guicontext.CommitDialogView{Set: set, Conn: &models.Connection{Password: secret}, Mode: guicontext.CommitDialogDryRunResult, DryRunResult: []guicontext.DryRunStmtResult{{SQL: batch[0].SQL, RowsAffected: 1}}})
	if strings.Contains(body, secret) || strings.Contains(body, hex.EncodeToString([]byte(secret))) || !strings.Contains(body, hex.EncodeToString([]byte("***"))) || !reflect.DeepEqual(set.Edits(), before) {
		t.Fatal("dry-run display SQL encoded a recoverable binary credential")
	}
}

func TestCommitPreviewBatchKeepsOriginalRowAffectedIndexes(t *testing.T) {
	edits := []models.PendingEdit{
		{PrimaryKey: []any{"secret"}, Column: "z", OldValue: "old", NewValue: "new"},
		{PrimaryKey: []any{"***"}, Column: "a", OldValue: "old", NewValue: "new"},
		{PrimaryKey: []any{"secret"}, Column: "b", OldValue: "old", NewValue: "new"},
	}
	batch, err := BuildCommitPreviewBatch(models.Ref{Table: "items"}, []string{"id"}, edits, func(text string) string { return strings.ReplaceAll(text, "secret", "***") })
	if err != nil || len(batch) != 2 || len(batch[0].EditIndexes) != 2 || !reflect.DeepEqual(batch[1].EditIndexes, []int{1}) {
		t.Fatalf("original edit indexes were lost: %v %v", batch, err)
	}
	indexes := append([]int(nil), batch[0].EditIndexes...)
	if (indexes[0] != 0 || indexes[1] != 2) && (indexes[0] != 2 || indexes[1] != 0) {
		t.Fatal("column sorting remapped rows-affected counts to another row")
	}
}

func TestCommitPreviewEscapingCollectionsAndKeyCollisions(t *testing.T) {
	for _, secret := range []string{`x\y`, "quote'password", "猫", strings.Repeat("long-password", 6)} {
		for _, mode := range []guicontext.CommitDialogMode{guicontext.CommitDialogPreview, guicontext.CommitDialogSqlPreview, guicontext.CommitDialogDryRunResult} {
			set := &models.PendingEditSet{Table: models.Ref{Schema: "app", Table: "items"}}
			jsonValue, _ := json.Marshal(map[string]any{"value": secret})
			if secret == "猫" {
				jsonValue = []byte(`{"value":"\u732b"}`)
			}
			for _, edit := range []models.PendingEdit{
				{PrimaryKey: []any{secret}, Column: "payload", ColumnType: "jsonb", OldValue: jsonValue, NewValue: jsonValue},
				{PrimaryKey: []any{"***"}, Column: "payload", ColumnType: "jsonb", OldValue: jsonValue, NewValue: jsonValue},
				{PrimaryKey: []any{secret}, Column: "values", ColumnType: "text[]", OldValue: []string{secret}, NewValue: []string{secret}},
			} {
				if err := set.Add(edit); err != nil {
					t.Fatal(err)
				}
			}
			encoded := strings.NewReplacer("'", "''", "\\", "\\\\").Replace(secret)
			before := set.Edits()
			view := guicontext.CommitDialogView{Set: set, Conn: &models.Connection{Password: secret}, Mode: mode, PkCols: []string{"id"}, DryRunResult: []guicontext.DryRunStmtResult{{SQL: "UPDATE items SET value=E'" + encoded + "'"}}}
			body := DefaultCommitDialogRender(view)
			if strings.Contains(body, secret) || strings.Contains(body, encoded) || strings.Contains(body, `\u732b`) || strings.Contains(body, "long-passwordlong-password") || !strings.Contains(body, "***") {
				t.Fatalf("synthetic secret %q mode %v: SQL escaping, byte JSON or typed array escaped credential redaction; body %q", secret, mode, body)
			}
			if !strings.Contains(body, "Commit 3 changes") || !reflect.DeepEqual(set.Edits(), before) {
				t.Fatal("redaction merged changes with colliding display keys or changed originals")
			}
			if mode == guicontext.CommitDialogPreview && strings.Count(body, "row ") != 2 {
				t.Fatal("original row grouping was not preserved across redaction collisions")
			}
		}
	}
}
