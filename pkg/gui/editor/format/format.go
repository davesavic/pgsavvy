// Package format provides SQL formatting via sqlfmt.
package format

import (
	"strings"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
	"github.com/mjibson/sqlfmt"
)

// Format pretty-prints one or more semicolon-separated SQL statements.
// Returns the formatted SQL or an error if any statement fails to parse.
// Tabs in sqlfmt output are replaced with spaces so that buffer rune
// positions match view cell positions (gocui expands tabs visually).
func Format(sql string) (string, error) {
	cfg := tree.DefaultPrettyCfg()
	cfg.UseTabs = false
	out, err := sqlfmt.FmtSQL(cfg, []string{sql})
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(out, "\t", "    "), nil
}
