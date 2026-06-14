package commands

import "reflect"

// Handler is the universal handler signature for every action in the
// CommandRegistry. All ~49 existing controller handlers migrate to
// this shape (per epic decision D2).
type Handler = func(ExecCtx) error

// Command is one named, dispatchable action.
//
// ID is the stable action identifier used in config.yml (e.g.
// "table.refresh") and looked up by the Matcher at dispatch time.
//
// Description is a one-line human label rendered in the cheatsheet
// and which-key popup.
//
// Tag groups related commands in the cheatsheet (e.g. "Query",
// "Result Grid"). The grouping is purely cosmetic.
//
// Handler is the executable; never nil for a registered Command.
//
// GetDisabled is an optional dynamic predicate evaluated by
// Matcher.Dispatch immediately after lookup. When it returns
// (reason, true) the Handler is NOT invoked; the Matcher emits a
// toast carrying reason instead and reports the dispatch as
// Dispatched (the key was consumed). nil means "always enabled"
// (subject to DisabledReasonStatic).
//
// DisabledReasonStatic is a fixed disable reason honoured when
// GetDisabled is nil: a non-empty string disables the binding
// statically. Useful for commands flipped at registration time
// based on driver capabilities (e.g. "live cancel not supported"
// on backends without CancelRequest).
//
// Resolution order (see Disabled): GetDisabled wins when non-nil;
// otherwise DisabledReasonStatic — empty string means enabled.
type Command struct {
	ID                   string
	Description          string
	Tag                  string
	Handler              Handler
	GetDisabled          func(ExecCtx) (reason string, disabled bool)
	DisabledReasonStatic string
}

// Disabled reports whether the command is currently disabled and, if
// so, the user-facing reason string. Resolution order:
//
//  1. If GetDisabled is non-nil, its return value wins. A panic
//     inside GetDisabled is recovered and the command is reported
//     as disabled with reason "<internal error>" so a buggy
//     predicate cannot crash the Matcher.
//  2. Otherwise DisabledReasonStatic — a non-empty string disables
//     the binding, an empty string leaves it enabled.
//
// Safe to call with the zero ExecCtx (used by options_bar at frame
// build time, when no dispatch context is available).
func (c Command) Disabled(ctx ExecCtx) (reason string, disabled bool) {
	if c.GetDisabled != nil {
		defer func() {
			if r := recover(); r != nil {
				reason = "<internal error>"
				disabled = true
			}
		}()
		return c.GetDisabled(ctx)
	}
	if c.DisabledReasonStatic != "" {
		return c.DisabledReasonStatic, true
	}
	return "", false
}

// nopHandler is the single concrete <nop> handler. All <nop> bindings
// share this exact function value so IsNop can identify them via
// reflect.Value.Pointer comparison.
var nopHandler Handler = func(ExecCtx) error { return nil }

// NopSentinel is the public Handler value that <nop> / <disabled>
// bindings carry. Callers must NOT compare arbitrary Handlers with
// `==` (Go forbids it for func types); use IsNop instead.
var NopSentinel Handler = nopHandler

// NopCommand is the canonical *Command wrapper for the <nop> sentinel.
// Trie nodes for explicitly-unbound keys point at this exact value,
// so source-tag rendering can identify them via pointer comparison
// (`cmd == NopCommand`) without needing IsNop.
var NopCommand = &Command{
	ID:          "<nop>",
	Description: "(unbound)",
	Tag:         "",
	Handler:     NopSentinel,
}

// IsNop reports whether h is the <nop> sentinel Handler.
//
// Implementation note: Go does not permit `==` on func values
// (compiler error), so we compare the underlying code pointers via
// reflect. This is reliable when both sides reference the same
// package-level var (NopSentinel) — the compiler never duplicates
// the function value behind a var declaration.
func IsNop(h Handler) bool {
	if h == nil {
		return false
	}
	return reflect.ValueOf(h).Pointer() == reflect.ValueOf(NopSentinel).Pointer()
}
