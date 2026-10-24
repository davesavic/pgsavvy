package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxInputBytes   = 1 << 20
	MaxOutputBytes  = 1 << 20
	MaxParameters   = 256
	MaxProfileBytes = 256
	DefaultRows     = 100
	MaxRows         = 1000
	MaxCellBytes    = 64 << 10
)

// executionMetadata is supplied explicitly by full callers, never inferred from
// success or delivery: neither proves that database effects were durable.
type executionMetadata struct {
	Status                        string `json:"status"`
	EffectsMayHaveOccurred        bool   `json:"effects_may_have_occurred"`
	PreparedTransactionsMayRemain bool   `json:"prepared_transactions_may_remain,omitempty"`
}

type Envelope struct {
	SchemaVersion    int                `json:"schema_version"`
	OK               bool               `json:"ok"`
	Command          string             `json:"command"`
	Connection       string             `json:"connection,omitempty"`
	Data             any                `json:"data,omitempty"`
	Error            *PublicError       `json:"error,omitempty"`
	Warnings         []ErrorCode        `json:"warnings"`
	ResultComplete   bool               `json:"result_complete"`
	Truncated        bool               `json:"truncated"`
	TruncationReason string             `json:"truncation_reason,omitempty"`
	Execution        *executionMetadata `json:"execution,omitempty"`
}

type limitedBuffer struct {
	bytes.Buffer
	ctx context.Context
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > MaxOutputBytes-b.Len() {
		return 0, &PublicError{Code: ResourceFailure}
	}
	return b.Buffer.Write(p)
}

// encode finishes before publication, so encoding failure cannot publish a prefix.
func encode(envelope Envelope) ([]byte, error) {
	return encodeContext(context.Background(), envelope)
}

func encodeContext(ctx context.Context, envelope Envelope) ([]byte, error) {
	envelope.SchemaVersion = 1
	if envelope.Execution != nil {
		metadata := *envelope.Execution
		switch metadata.Status {
		case "not_started", "completed", "failed", "unknown":
		default:
			metadata.Status = "unknown"
		}
		envelope.Execution = &metadata
	}
	if envelope.OK {
		if envelope.Error != nil {
			return nil, &PublicError{Code: ResourceFailure}
		}
		if envelope.Data == nil {
			envelope.Data = struct{}{}
		}
	} else {
		if envelope.Error == nil {
			return nil, &PublicError{Code: ResourceFailure}
		}
		envelope.Data = nil
		envelope.ResultComplete = false
		envelope.Truncated = false
		envelope.TruncationReason = ""
	}
	if envelope.TruncationReason != "" && envelope.TruncationReason != "row_limit" && envelope.TruncationReason != "page_limit" && (envelope.Execution == nil || envelope.TruncationReason != "result_limit") {
		return nil, &PublicError{Code: ResourceFailure}
	}
	if envelope.Warnings == nil {
		envelope.Warnings = []ErrorCode{}
	}
	if len(envelope.Warnings) > MaxOutputBytes/10 {
		return nil, &PublicError{Code: ResourceFailure}
	}
	for _, warning := range envelope.Warnings {
		if _, ok := publicErrors[warning]; !ok && warning != HistoryUnavailable && warning != HistoryUpgradeRequired {
			return nil, &PublicError{Code: ResourceFailure}
		}
	}
	if len(envelope.Command) > MaxProfileBytes || len(envelope.Connection) > MaxProfileBytes || !utf8.ValidString(envelope.Command) || !utf8.ValidString(envelope.Connection) {
		return nil, &PublicError{Code: ResourceFailure}
	}
	budget := MaxOutputBytes
	data, err := normalizeData(ctx, reflect.ValueOf(envelope.Data), &budget, 0)
	if err != nil {
		return nil, err
	}
	envelope.Data = data
	buffer := limitedBuffer{ctx: ctx}
	wire := struct {
		Envelope
		Data *any `json:"data,omitempty"`
	}{Envelope: envelope}
	if envelope.OK {
		wire.Data = &envelope.Data
	}
	if err := json.NewEncoder(&buffer).Encode(wire); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &PublicError{Code: ResourceFailure}
	}
	if !utf8.Valid(buffer.Bytes()) {
		return nil, &PublicError{Code: ResourceFailure}
	}
	return buffer.Bytes(), nil
}

func encodeWithOptionalHistoryWarnings(ctx context.Context, envelope Envelope) ([]byte, error) {
	data, err := encodeContext(ctx, envelope)
	if err == nil {
		return data, nil
	}
	warnings := make([]ErrorCode, 0, len(envelope.Warnings))
	for _, warning := range envelope.Warnings {
		if warning != HistoryUnavailable && warning != HistoryUpgradeRequired {
			warnings = append(warnings, warning)
		}
	}
	if len(warnings) == len(envelope.Warnings) {
		return nil, err
	}
	// An optional history warning alone cannot invalidate an otherwise fitting
	// query result at the output limit. Other warnings retain their contract.
	envelope.Warnings = warnings
	return encodeContext(ctx, envelope)
}

func normalizeData(ctx context.Context, v reflect.Value, budget *int, depth int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fail := func() (any, error) { return nil, &PublicError{Code: ResourceFailure} }
	charge := func(n int) bool {
		if n > *budget {
			return false
		}
		*budget -= n
		return true
	}
	if depth > 64 {
		return fail()
	}
	if !v.IsValid() {
		if !charge(4) {
			return fail()
		}
		return nil, nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			if !charge(4) {
				return fail()
			}
			return nil, nil
		}
		return normalizeData(ctx, v.Elem(), budget, depth+1)
	case reflect.String:
		s := v.String()
		cost := len(s) + 2
		if v.Type() == reflect.TypeFor[json.Number]() {
			cost = len(s)
		}
		if cost > *budget || !utf8.ValidString(s) || !charge(cost) {
			return fail()
		}
		if v.Type() == reflect.TypeFor[json.Number]() {
			return json.Number(s), nil
		}
		return s, nil
	case reflect.Bool:
		cost := 5
		if v.Bool() {
			cost = 4
		}
		if !charge(cost) {
			return fail()
		}
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !charge(len(strconv.FormatInt(v.Int(), 10))) {
			return fail()
		}
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !charge(len(strconv.FormatUint(v.Uint(), 10))) {
			return fail()
		}
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		if !charge(1) {
			return fail()
		}
		return v.Float(), nil
	case reflect.Slice, reflect.Array:
		if v.Len() > *budget/2 || !charge(2) {
			return fail()
		}
		items := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			if i > 0 && !charge(1) {
				return fail()
			}
			item, err := normalizeData(ctx, v.Index(i), budget, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Len() > *budget/4 || !charge(2) {
			return fail()
		}
		items := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			cost := len(key) + 3
			if len(items) > 0 {
				cost++
			}
			if cost > *budget || !utf8.ValidString(key) || !charge(cost) {
				return fail()
			}
			value, err := normalizeData(ctx, iter.Value(), budget, depth+1)
			if err != nil {
				return nil, err
			}
			items[key] = value
		}
		return items, nil
	case reflect.Struct:
		if !charge(2) {
			return fail()
		}
		items := map[string]any{}
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			parts := strings.Split(field.Tag.Get("json"), ",")
			name := parts[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			value := v.Field(i)
			if len(parts) > 1 && parts[1] == "omitempty" && (value.IsZero() || ((value.Kind() == reflect.Slice || value.Kind() == reflect.Map) && value.Len() == 0)) {
				continue
			}
			cost := len(name) + 3
			if len(items) > 0 {
				cost++
			}
			if !charge(cost) {
				return fail()
			}
			item, err := normalizeData(ctx, value, budget, depth+1)
			if err != nil {
				return nil, err
			}
			items[name] = item
		}
		return items, nil
	default:
		return fail()
	}
}

// WriteJSON owns publication until completion. Close on the injected writer must
// interrupt a blocked Write; non-cooperating writers are outside this contract.
func WriteJSON(ctx context.Context, out io.WriteCloser, envelope Envelope) error {
	data, err := encodeContext(ctx, envelope)
	if err != nil {
		return err
	}
	return publish(ctx, out, data)
}

func publish(ctx context.Context, out io.WriteCloser, data []byte) error {
	if out == nil {
		return &PublicError{Code: ResourceFailure}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = out.Close()
		case <-stop:
		}
	}()
	n, err := out.Write(data)
	close(stop)
	<-joined
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || n != len(data) {
		return &PublicError{Code: ResourceFailure}
	}
	return nil
}
