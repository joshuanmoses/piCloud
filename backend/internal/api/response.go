package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/joshuanmoses/piCloud/backend/internal/logging"
)

// ErrorCode is a stable, machine-readable error identifier. The frontend
// branches on these; the human-readable message may be reworded freely.
type ErrorCode string

// The complete set of error codes the API returns.
const (
	CodeBadRequest             ErrorCode = "bad_request"
	CodeValidationFailed       ErrorCode = "validation_failed"
	CodeUnauthorized           ErrorCode = "unauthorized"
	CodeForbidden              ErrorCode = "forbidden"
	CodeNotFound               ErrorCode = "not_found"
	CodeConflict               ErrorCode = "conflict"
	CodePayloadTooLarge        ErrorCode = "payload_too_large"
	CodeUnsupportedMedia       ErrorCode = "unsupported_media_type"
	CodeRateLimited            ErrorCode = "rate_limited"
	CodeQuotaExceeded          ErrorCode = "quota_exceeded"
	CodeAccountLocked          ErrorCode = "account_locked"
	CodeAccountDisabled        ErrorCode = "account_disabled"
	CodeCSRFFailed             ErrorCode = "csrf_failed"
	CodePasswordChangeRequired ErrorCode = "password_change_required"
	CodeInternal               ErrorCode = "internal_error"
	CodeUnavailable            ErrorCode = "service_unavailable"
	CodeNotImplemented         ErrorCode = "not_implemented"
)

// ErrorBody is the error half of a failed response.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Fields carries per-field validation messages. It is omitted unless the
	// error is a validation failure.
	Fields map[string]string `json:"fields,omitempty"`
}

// ErrorResponse is the envelope returned for every non-2xx API response.
type ErrorResponse struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

// APIError is an error that carries the HTTP status and public message to
// return. Handlers construct these; the transport layer renders them.
//
// The distinction that matters: Message is shown to the user, cause is not.
// A database error becomes an APIError with a generic message, while the real
// error is logged with the request ID for the operator to correlate.
type APIError struct {
	Status  int
	Code    ErrorCode
	Message string
	Fields  map[string]string
	cause   error
}

func (e *APIError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (e *APIError) Unwrap() error { return e.cause }

// WithCause attaches an internal error for logging. The cause is never sent to
// the client.
func (e *APIError) WithCause(err error) *APIError {
	clone := *e
	clone.cause = err
	return &clone
}

// WithField adds a per-field validation message.
func (e *APIError) WithField(field, message string) *APIError {
	clone := *e
	clone.Fields = make(map[string]string, len(e.Fields)+1)
	for k, v := range e.Fields {
		clone.Fields[k] = v
	}
	clone.Fields[field] = message
	return &clone
}

// Constructors for the errors handlers raise.

// ErrBadRequest reports a malformed request.
func ErrBadRequest(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: msg}
}

// ErrValidation reports failed input validation.
func ErrValidation(msg string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeValidationFailed, Message: msg}
}

// ErrUnauthorized reports missing or invalid authentication.
func ErrUnauthorized(msg string) *APIError {
	if msg == "" {
		msg = "Authentication is required."
	}
	return &APIError{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: msg}
}

// ErrForbidden reports an authenticated caller lacking permission.
//
// Note for later phases: prefer ErrNotFound when revealing that a resource
// exists would itself be a disclosure.
func ErrForbidden(msg string) *APIError {
	if msg == "" {
		msg = "You do not have permission to perform this action."
	}
	return &APIError{Status: http.StatusForbidden, Code: CodeForbidden, Message: msg}
}

// ErrNotFound reports a missing resource. The message is deliberately uniform
// so it cannot be used to probe for the existence of other users' content.
func ErrNotFound() *APIError {
	return &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: "The requested resource was not found."}
}

// ErrConflict reports a state conflict, such as a duplicate name.
func ErrConflict(msg string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: msg}
}

// ErrTooLarge reports an oversized payload.
func ErrTooLarge(msg string) *APIError {
	return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodePayloadTooLarge, Message: msg}
}

// ErrRateLimited reports throttling.
func ErrRateLimited(msg string) *APIError {
	if msg == "" {
		msg = "Too many requests. Please slow down and try again shortly."
	}
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: msg}
}

// ErrInternal reports an unexpected server failure. The cause is logged, never
// returned.
func ErrInternal(cause error) *APIError {
	return &APIError{
		Status:  http.StatusInternalServerError,
		Code:    CodeInternal,
		Message: "An unexpected error occurred. Please try again.",
		cause:   cause,
	}
}

// ErrUnavailable reports a dependency being temporarily unusable.
func ErrUnavailable(msg string) *APIError {
	if msg == "" {
		msg = "The service is temporarily unavailable. Please try again shortly."
	}
	return &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: msg}
}

// WriteJSON serializes v as JSON with the given status.
//
// It encodes into a buffer first: encoding straight to the ResponseWriter would
// emit a 200 header and then fail midway through, producing a truncated body
// that the client cannot distinguish from a successful response.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		logging.FromContext(r.Context()).Error("response serialization failed",
			"error", err, "type", fmt.Sprintf("%T", v))
		writeRawError(w, r, http.StatusInternalServerError, ErrorBody{
			Code:    CodeInternal,
			Message: "An unexpected error occurred. Please try again.",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		if _, err := w.Write(body); err != nil {
			// The client hung up mid-response. Debug level: this is normal.
			logging.FromContext(r.Context()).Debug("response write failed", "error", err)
		}
	}
}

// WriteNoContent sends 204.
func WriteNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// WriteError renders err as a JSON error envelope, logging internal failures
// with their real cause.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = ErrInternal(err)
	}

	logger := logging.FromContext(r.Context())
	if apiErr.Status >= http.StatusInternalServerError {
		attrs := []any{
			slog.String("code", string(apiErr.Code)),
			slog.Int("status", apiErr.Status),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		}
		if apiErr.cause != nil {
			attrs = append(attrs, slog.String("error", apiErr.cause.Error()))
		}
		logger.Error("request failed", attrs...)
	} else {
		logger.Debug("request rejected",
			"code", string(apiErr.Code), "status", apiErr.Status, "message", apiErr.Message)
	}

	writeRawError(w, r, apiErr.Status, ErrorBody{
		Code:    apiErr.Code,
		Message: apiErr.Message,
		Fields:  apiErr.Fields,
	})
}

func writeRawError(w http.ResponseWriter, r *http.Request, status int, body ErrorBody) {
	resp := ErrorResponse{Error: body, RequestID: RequestIDFromContext(r.Context())}
	payload, err := json.Marshal(resp)
	if err != nil {
		// Last resort: a hand-built envelope that cannot fail to serialize.
		payload = []byte(`{"error":{"code":"internal_error","message":"An unexpected error occurred."}}`)
		status = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Error responses must never be cached; a 401 cached by an intermediary
	// would lock a user out after they log in.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload)
	}
}

// DecodeJSON reads a JSON request body into dst with the guards every JSON
// endpoint needs: a size limit, strict field checking, and exactly one value.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
		return &APIError{
			Status:  http.StatusUnsupportedMediaType,
			Code:    CodeUnsupportedMedia,
			Message: "Request body must be application/json.",
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	// Reject unknown fields: silently ignoring them hides client bugs and can
	// mask a parameter-smuggling attempt.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return jsonDecodeError(err)
	}
	// A second value in the stream means the client sent something unexpected.
	// Decoding into json.RawMessage accepts any shape, so the only non-error
	// outcome that means "clean end of body" is io.EOF. Decoding into an empty
	// struct would not work here: DisallowUnknownFields makes it fail on any
	// trailing object, which is indistinguishable from the stream being done.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return ErrBadRequest("Request body must contain a single JSON object.")
	}
	return nil
}

func jsonDecodeError(err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.As(err, &maxErr):
		return ErrTooLarge("Request body is too large.")
	case errors.As(err, &syntaxErr):
		return ErrBadRequest(fmt.Sprintf("Request body contains malformed JSON at position %d.", syntaxErr.Offset))
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return ErrBadRequest(fmt.Sprintf("Field %q has the wrong type.", typeErr.Field))
		}
		return ErrBadRequest("Request body contains a value of the wrong type.")
	case errors.Is(err, io.EOF):
		return ErrBadRequest("Request body must not be empty.")
	default:
		// json's "unknown field" error text is safe to surface: it names only
		// what the client itself sent.
		if strings.Contains(strings.ToLower(err.Error()), "unknown field") {
			return ErrBadRequest("Request body contains an unrecognized field.")
		}
		return ErrBadRequest("Request body could not be parsed.")
	}
}

func isJSONContentType(ct string) bool {
	// Strip parameters such as "; charset=utf-8".
	mediaType, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}
