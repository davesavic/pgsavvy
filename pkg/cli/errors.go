package cli

import (
	"context"
	"encoding/json"
	"errors"
)

const (
	ExitSuccess     = 0
	ExitInput       = 2
	ExitUnavailable = 3
	ExitPolicy      = 4
	ExitDatabase    = 5
	ExitTimeout     = 6
	ExitResource    = 7
)

type ErrorCode string

const (
	InvalidInput              ErrorCode = "INVALID_INPUT"
	UnsupportedSQL            ErrorCode = "UNSUPPORTED_SQL"
	ConnectionUnavailable     ErrorCode = "CONNECTION_UNAVAILABLE"
	CredentialsUnavailable    ErrorCode = "CREDENTIALS_UNAVAILABLE"
	TrustRequired             ErrorCode = "TRUST_REQUIRED"
	UnsupportedPgpassFailover ErrorCode = "UNSUPPORTED_PGPASS_FAILOVER"
	PolicyDenied              ErrorCode = "POLICY_DENIED"
	ApprovalRequired          ErrorCode = "APPROVAL_REQUIRED"
	ApprovalMismatch          ErrorCode = "APPROVAL_MISMATCH"
	ApprovalUnavailable       ErrorCode = "APPROVAL_UNAVAILABLE"
	ApprovalCancelled         ErrorCode = "APPROVAL_CANCELLED"
	IncompleteTransaction     ErrorCode = "INCOMPLETE_TRANSACTION"
	DatabaseFailure           ErrorCode = "DATABASE_FAILURE"
	ObjectNotFound            ErrorCode = "OBJECT_NOT_FOUND"
	Timeout                   ErrorCode = "TIMEOUT"
	ResourceFailure           ErrorCode = "RESOURCE_FAILURE"
	HistoryUnavailable        ErrorCode = "HISTORY_UNAVAILABLE"
	HistoryUpgradeRequired    ErrorCode = "HISTORY_UPGRADE_REQUIRED"
)

type errorDescription struct {
	exit                 int
	message, remediation string
}

var publicErrors = map[ErrorCode]errorDescription{
	InvalidInput:              {ExitInput, "Invalid command or input.", "Consult command help."},
	UnsupportedSQL:            {ExitInput, "SQL syntax is not supported for machine queries.", "Use one supported read-only SELECT, WITH, VALUES or TABLE statement."},
	ConnectionUnavailable:     {ExitUnavailable, "Connection is unavailable.", "Check the selected trusted profile and database availability."},
	CredentialsUnavailable:    {ExitUnavailable, "Noninteractive credentials are unavailable.", "Provision a noninteractive credential source in the trusted profile."},
	TrustRequired:             {ExitUnavailable, "SSH trust verification failed.", "Provision and verify the bastion key in the existing known_hosts file."},
	UnsupportedPgpassFailover: {ExitUnavailable, "Explicit pgpass credentials differ across fallback endpoints.", "Use a single-endpoint profile or consistent credentials."},
	PolicyDenied:              {ExitPolicy, "The selected profile does not permit this operation.", "Explicitly configure the required cli_access in trusted configuration."},
	ApprovalRequired:          {ExitPolicy, "Invocation approval is required.", "Approve the current invocation using the connection confirmation ceremony or explicit approval flags."},
	ApprovalMismatch:          {ExitPolicy, "Connection confirmation does not match.", "Confirm the exact selected connection name for this invocation."},
	ApprovalUnavailable:       {ExitPolicy, "Invocation approval is unavailable.", "Use an available controlling terminal or explicit approval flags."},
	ApprovalCancelled:         {ExitPolicy, "Invocation approval was declined or cancelled.", "Review the requested SQL before starting a new invocation."},
	IncompleteTransaction:     {ExitDatabase, "SQL left an incomplete transaction.", "Finish local transactions explicitly; inspect possible effects before any replay."},
	DatabaseFailure:           {ExitDatabase, "Database operation failed.", "Check database permissions and supported input."},
	ObjectNotFound:            {ExitDatabase, "The requested database object does not exist.", "Check exact schema and table names."},
	Timeout:                   {ExitTimeout, "Invocation cancelled or timed out.", "Reduce the requested work and check service availability."},
	ResourceFailure:           {ExitResource, "Input, output or result resource failure.", "Check I/O availability and fixed input/result limits."},
}

// PublicError contains only allowlisted output, never backend error text.
type PublicError struct {
	Code     ErrorCode
	SQLState string
}

func (e *PublicError) description() errorDescription {
	if d, ok := publicErrors[e.Code]; ok {
		return d
	}
	return publicErrors[ResourceFailure]
}

func (e *PublicError) Error() string { return e.description().message }

func (e *PublicError) MarshalJSON() ([]byte, error) {
	code := e.Code
	if _, ok := publicErrors[code]; !ok {
		code = ResourceFailure
	}
	state := e.SQLState
	if len(state) != 5 {
		state = ""
	}
	for _, c := range state {
		if c < '0' || c > '9' {
			if c < 'A' || c > 'Z' {
				state = ""
				break
			}
		}
	}
	d := e.description()
	return json.Marshal(struct {
		Code        ErrorCode `json:"code"`
		Message     string    `json:"message"`
		Remediation string    `json:"remediation"`
		SQLState    string    `json:"sqlstate,omitempty"`
	}{code, d.message, d.remediation, state})
}

func safeError(err error) *PublicError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &PublicError{Code: Timeout}
	}
	var public *PublicError
	if errors.As(err, &public) && public != nil {
		if _, ok := publicErrors[public.Code]; ok {
			return public
		}
	}
	return &PublicError{Code: ResourceFailure}
}
