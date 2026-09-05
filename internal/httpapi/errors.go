package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorCode is a stable, machine-readable error identifier. Once shipped a code is part of
// the API contract and must not be renamed or repurposed within a major version.
// The catalogue lives in specs/001-qr-service-baseline/contracts/errors.md.
type ErrorCode string

const (
	CodeInvalidRequest             ErrorCode = "invalid_request"
	CodeContentTooLarge            ErrorCode = "content_too_large"
	CodeUnsupportedScheme          ErrorCode = "unsupported_scheme"
	CodeSelfReferentialDestination ErrorCode = "self_referential_destination"
	CodeAliasInvalid               ErrorCode = "alias_invalid"
	CodeAliasReserved              ErrorCode = "alias_reserved"
	CodeAliasTaken                 ErrorCode = "alias_taken"
	CodeContrastTooLow             ErrorCode = "contrast_too_low"
	CodeLogoTooLarge               ErrorCode = "logo_too_large"
	CodeDimensionsExceeded         ErrorCode = "dimensions_exceeded"
	CodeRenderTimeout              ErrorCode = "render_timeout"
	CodeNotFound                   ErrorCode = "not_found"
	CodeCodeDisabled               ErrorCode = "code_disabled"
	CodeUnauthorized               ErrorCode = "unauthorized"
	CodeForbidden                  ErrorCode = "forbidden"
	CodeTokenRevoked               ErrorCode = "token_revoked"
	CodeConflict                   ErrorCode = "conflict"
	CodeDirectCodeImmutable        ErrorCode = "direct_code_immutable" // spec 002
	CodeNotTracked                 ErrorCode = "not_tracked"           // spec 002
	CodeBatchTooLarge              ErrorCode = "batch_too_large"       // spec 003
	CodeClientRefConflict          ErrorCode = "client_ref_conflict"   // spec 003
	CodeRateLimited                ErrorCode = "rate_limited"
	CodeInternal                   ErrorCode = "internal"
	CodeNotImplemented             ErrorCode = "not_implemented" // foundation stubs only
)

// Status returns the HTTP status conventionally paired with a code.
func (c ErrorCode) Status() int {
	switch c {
	case CodeContentTooLarge, CodeBatchTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeAliasReserved, CodeAliasTaken, CodeConflict, CodeDirectCodeImmutable, CodeClientRefConflict:
		return http.StatusConflict
	case CodeNotFound:
		return http.StatusNotFound
	case CodeCodeDisabled:
		return http.StatusGone
	case CodeUnauthorized, CodeTokenRevoked:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeInternal:
		return http.StatusInternalServerError
	case CodeNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusBadRequest
	}
}

// ErrorBody is the single error envelope every endpoint uses (FR-044).
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable code, a safe human message, and optional structured details.
type ErrorDetail struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// WriteError writes the envelope. The message MUST be a safe sentence composed by the
// caller — never err.Error() from a driver, never a path, never SQL. Internal causes are
// logged with the request ID by the caller, not returned.
func WriteError(w http.ResponseWriter, code ErrorCode, msg string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code.Status())
	if err := json.NewEncoder(w).Encode(ErrorBody{Error: ErrorDetail{Code: code, Message: msg, Details: details}}); err != nil {
		slog.Debug("httpapi: writing error body", "err", err)
	}
}

// WriteJSON writes a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("httpapi: writing json body", "err", err)
	}
}

// Internal logs the real cause with context and returns a bland 500 to the client.
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error", "err", err, "route", r.Pattern)
	WriteError(w, CodeInternal, "An unexpected error occurred.", nil)
}
