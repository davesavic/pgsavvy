package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	InvocationTimeout = 30 * time.Second
	ConnectionTimeout = 10 * time.Second
	HelperTimeout     = 5 * time.Second
	StatementTimeout  = 10 * time.Second
	LockTimeout       = time.Second
	CleanupTimeout    = 2 * time.Second
	MaxHelperBytes    = 32 << 10
)

// IO handles belong to one invocation. Close must interrupt concurrent I/O.
type IO struct {
	In       io.ReadCloser
	Out, Err io.WriteCloser
	// Cancel interrupts operation/input work without prematurely closing output.
	Cancel <-chan struct{}
}

// Command returns only after its backend operation and cleanup have completed.
type Command func(context.Context, []string, IO) (Envelope, error)

type cleanupBudgetKey struct{}

// CleanupBudget is fresh time reserved inside the invocation's outer deadline.
func CleanupBudget(ctx context.Context) time.Duration {
	if budget, ok := ctx.Value(cleanupBudgetKey{}).(time.Duration); ok {
		return budget
	}
	return CleanupTimeout
}

// IsLegacyRoute recognizes valid legacy route words without admitting ignored
// positional command words into the TUI. The legacy app still validates values.
func IsLegacyRoute(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "update" {
		return true
	}
	for i := 0; i < len(args); i++ {
		parts := strings.SplitN(args[i], "=", 2)
		switch parts[0] {
		case "--version", "-version":
		case "--log-dir", "-log-dir":
			if len(parts) == 1 {
				i++
				if i >= len(args) {
					return true
				}
			}
		case "--":
			return i == len(args)-1
		default:
			return false
		}
	}
	return true
}

// Dispatch never enters the legacy application. The caller composes its registry.
func Dispatch(parent context.Context, args []string, streams IO, registry map[string]Command) (bool, int) {
	if IsLegacyRoute(args) {
		return false, ExitSuccess
	}
	rest, timeout, flagErr := invocationFlags(args[1:])
	if flagErr != nil {
		timeout = InvocationTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	remaining := max(time.Duration(0), time.Until(deadline))
	reserve := min(2*CleanupTimeout, remaining*2/5)
	workCtx, cancelWork := context.WithDeadline(context.WithValue(ctx, cleanupBudgetKey{}, reserve/2), deadline.Add(-reserve))
	defer cancelWork()
	closeInput := sync.OnceFunc(func() {
		if streams.In != nil {
			_ = streams.In.Close()
		}
	})
	closeIO := sync.OnceFunc(func() {
		closeInput()
		if streams.Out != nil {
			_ = streams.Out.Close()
		}
		if streams.Err != nil {
			_ = streams.Err.Close()
		}
	})
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-streams.Cancel:
			cancelWork()
		case <-workCtx.Done():
		case <-stop:
			return
		}
		closeInput()
		select {
		case <-ctx.Done():
			closeIO()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-joined; closeIO() }()
	command, ok := registry[args[0]]
	label := "unknown"
	if ok && len(args[0]) <= MaxProfileBytes && utf8.ValidString(args[0]) {
		label = args[0]
	}
	envelope := Envelope{Command: "unknown"}
	var err error
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			err = &PublicError{Code: InvalidInput}
		} else {
			names := make([]string, 0, len(registry))
			for name := range registry {
				names = append(names, name)
			}
			sort.Strings(names)
			envelope = Envelope{Command: "help", Data: map[string]any{"commands": names}, ResultComplete: true}
		}
	} else if !ok || command == nil {
		err = &PublicError{Code: InvalidInput}
	} else {
		envelope.Command = args[0]
		err = flagErr
		if err == nil {
			envelope, err = command(workCtx, rest, streams)
			if envelope.Command == "" {
				envelope.Command = args[0]
			}
		}
	}
	if workCtx.Err() != nil {
		err = workCtx.Err()
	}
	if ctx.Err() != nil {
		return true, ExitTimeout
	}
	if err == nil && envelope.Error != nil {
		err = envelope.Error
	}
	exit := ExitSuccess
	if err != nil {
		public := safeError(err)
		envelope = Envelope{Command: envelope.Command, Connection: envelope.Connection, Error: public}
		exit = public.description().exit
	} else {
		envelope.OK = true
	}
	data, encodingErr := encodeContext(ctx, envelope)
	if encodingErr != nil {
		public := safeError(encodingErr)
		envelope = Envelope{Command: label, Error: public}
		exit = public.description().exit
		data, encodingErr = encodeContext(ctx, envelope)
		if encodingErr != nil {
			return true, safeError(encodingErr).description().exit
		}
	}
	// Publication is the result's commitment point. Signals received before it
	// publish timeout instead; a signal after it cannot replace a partial write.
	select {
	case <-streams.Cancel:
		if exit != ExitTimeout {
			envelope = Envelope{Command: envelope.Command, Connection: envelope.Connection, Error: &PublicError{Code: Timeout}}
			data, encodingErr = encodeContext(ctx, envelope)
			if encodingErr != nil {
				return true, ExitTimeout
			}
			exit = ExitTimeout
		}
	default:
	}
	publicationCtx, cancelPublication := context.WithTimeout(ctx, reserve/2)
	if err := publish(publicationCtx, streams.Out, data); err != nil {
		exit = ExitResource
		select {
		case <-streams.Cancel:
			exit = ExitTimeout
		default:
		}
	}
	cancelPublication()
	if streams.Err != nil {
		diagnosticCtx, cancelDiagnostic := context.WithTimeout(ctx, 50*time.Millisecond)
		_ = publish(diagnosticCtx, streams.Err, []byte(fmt.Sprintf("{\"command\":%q,\"exit\":%d}\n", envelope.Command, exit)))
		cancelDiagnostic()
	}
	return true, exit
}

// ParseFlags accepts exact long names and preserves positional names literally.
func ParseFlags(args []string, values, switches []string) (map[string]string, []string, error) {
	allowed := map[string]bool{}
	for _, name := range values {
		allowed[name] = true
	}
	for _, name := range switches {
		allowed[name] = false
	}
	flags := map[string]string{}
	positional := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		parts := strings.SplitN(arg, "=", 2)
		name := strings.TrimPrefix(parts[0], "--")
		takesValue, exists := allowed[name]
		if !strings.HasPrefix(arg, "--") || !exists {
			return nil, nil, &PublicError{Code: InvalidInput}
		}
		if _, duplicate := flags[name]; duplicate {
			return nil, nil, &PublicError{Code: InvalidInput}
		}
		value := "true"
		if takesValue {
			if len(parts) == 2 {
				value = parts[1]
			} else {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "--") {
					return nil, nil, &PublicError{Code: InvalidInput}
				}
				value = args[i]
			}
			if value == "" {
				return nil, nil, &PublicError{Code: InvalidInput}
			}
		} else if len(parts) == 2 {
			return nil, nil, &PublicError{Code: InvalidInput}
		}
		flags[name] = value
	}
	return flags, positional, nil
}

func invocationFlags(args []string) ([]string, time.Duration, error) {
	rest := make([]string, 0, len(args))
	seen := map[string]bool{}
	timeout := InvocationTimeout
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			rest = append(rest, arg)
			continue
		}
		parts := strings.SplitN(arg, "=", 2)
		key := parts[0]
		if seen[key] {
			return nil, 0, &PublicError{Code: InvalidInput}
		}
		seen[key] = true
		if key != "--timeout" && key != "--format" {
			rest = append(rest, arg)
			continue
		}
		value := ""
		if len(parts) == 2 {
			value = parts[1]
		} else {
			i++
			if i >= len(args) {
				return nil, 0, &PublicError{Code: InvalidInput}
			}
			value = args[i]
		}
		if key == "--format" {
			if value != "json" {
				return nil, 0, &PublicError{Code: InvalidInput}
			}
		} else {
			var err error
			timeout, err = time.ParseDuration(value)
			if err != nil || timeout <= 0 || timeout > InvocationTimeout {
				return nil, 0, &PublicError{Code: InvalidInput}
			}
		}
	}
	return rest, timeout, nil
}
