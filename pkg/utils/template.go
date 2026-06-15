package utils

import (
	"bytes"
	"fmt"
	"text/template"
)

// ResolveTemplate executes a text/template against data and returns the result.
// Caller is responsible for trust boundaries of both 'tmpl' and 'data'. The
// rendered output MUST NOT be passed unescaped to a shell, SQL driver, or any
// other interpreter without context-appropriate escaping. text/template
// provides no command-injection protection. See also: html/template for HTML
// contexts.
//
// ResolveTemplate never panics: any template parse or execute error is
// returned as ("", err).
func ResolveTemplate(tmpl string, data any) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("pgsavvy").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return buf.String(), nil
}
