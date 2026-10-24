//go:build linux || darwin

package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ApprovalRequest is a captured invocation, without parameters or credentials.
type ApprovalRequest struct {
	Connection, SQL, Schema                     string
	MaxRows, ParameterCount, StatementCount     int
	WritesApprovalRequired, DDLApprovalRequired bool
}

type ApprovalProvider func(context.Context, ApprovalRequest) error

// NewTerminalApprovalProvider takes ownership of each acquired handle. Close
// must interrupt and join concurrent I/O, as with the machineio stream contract.
func NewTerminalApprovalProvider(open func(context.Context) (io.ReadWriteCloser, error)) ApprovalProvider {
	return func(ctx context.Context, request ApprovalRequest) error {
		if ctx.Err() != nil {
			return &PublicError{Code: Timeout}
		}
		if open == nil || request.Connection == "" || len(request.Connection) > MaxProfileBytes || !utf8.ValidString(request.Connection) || strings.ContainsFunc(request.Connection, approvalControl) {
			return &PublicError{Code: ApprovalUnavailable}
		}
		terminal, err := open(ctx)
		if err != nil || terminal == nil {
			if terminal != nil {
				_ = terminal.Close()
			}
			if ctx.Err() != nil {
				return &PublicError{Code: Timeout}
			}
			return &PublicError{Code: ApprovalUnavailable}
		}
		closeTerminal := sync.OnceFunc(func() { _ = terminal.Close() })
		stop, joined := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(joined)
			select {
			case <-ctx.Done():
				closeTerminal()
			case <-stop:
			}
		}()
		err = terminalApproval(terminal, request)
		close(stop)
		closeTerminal()
		<-joined
		if ctx.Err() != nil {
			return &PublicError{Code: Timeout}
		}
		return err
	}
}

func terminalApproval(terminal io.ReadWriter, request ApprovalRequest) error {
	prompt := approvalPreview(request) + "Type the exact connection name: "
	if n, err := io.WriteString(terminal, prompt); err != nil || n != len(prompt) {
		return &PublicError{Code: ApprovalUnavailable}
	}
	name, err := approvalLine(terminal)
	if err != nil {
		return err
	}
	if name != request.Connection {
		return &PublicError{Code: ApprovalMismatch}
	}
	prompt = "Type yes to approve this invocation: "
	if n, err := io.WriteString(terminal, prompt); err != nil || n != len(prompt) {
		return &PublicError{Code: ApprovalUnavailable}
	}
	answer, err := approvalLine(terminal)
	if err != nil {
		return err
	}
	if answer != "yes" {
		return &PublicError{Code: ApprovalCancelled}
	}
	return nil
}

func approvalLine(reader io.Reader) (string, error) {
	var line strings.Builder
	var one [1]byte
	for line.Len() <= MaxProfileBytes+1 {
		n, err := reader.Read(one[:])
		if n == 1 {
			if one[0] == '\n' {
				return strings.TrimSuffix(line.String(), "\r"), nil
			}
			line.WriteByte(one[0])
		}
		if err == io.EOF {
			return "", &PublicError{Code: ApprovalCancelled}
		}
		if err != nil || n == 0 {
			return "", &PublicError{Code: ApprovalUnavailable}
		}
	}
	return "", &PublicError{Code: ApprovalMismatch}
}

func approvalControl(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

func approvalText(value string, limit int) string {
	const omitted = "...[content omitted]"
	var out strings.Builder
	for _, r := range value {
		text := string(r)
		if approvalControl(r) || !unicode.IsPrint(r) {
			text = strconv.QuoteRuneToASCII(r)
			text = text[1 : len(text)-1]
		}
		if out.Len()+len(text) > limit-len(omitted) {
			out.WriteString(omitted)
			break
		}
		out.WriteString(text)
	}
	return out.String()
}

func approvalPreview(request ApprovalRequest) string {
	header := fmt.Sprintf("Invocation approval\nConnection: %s\nSchema: %s\nstatements=%d parameters=%d max_rows=%d writes=%t ddl=%t\nSQL (parameter values omitted): ",
		approvalText(request.Connection, 512), approvalText(request.Schema, 256), request.StatementCount, request.ParameterCount, request.MaxRows, request.WritesApprovalRequired, request.DDLApprovalRequired)
	return header + approvalText(request.SQL, 4096-len(header)-1) + "\n"
}
