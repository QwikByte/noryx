// Package httpapi contains the JSON, download and log stream helpers shared by the REST handlers
// of the master.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxBodyBytes = 1 << 20

// statusClientClosed is nginx's status of requests whose client went away before the answer.
const statusClientClosed = 499

// Error is an error whose message is safe to show to API clients. Code, if set, tells the
// panel what to offer, e.g. to confirm a request. Field, if set, is the JSON key of the field
// of the request it is about, with dots between the keys of nested objects, e.g.
// newServers.memoryMb, so that the panel shows it there.
type Error struct {
	Status  int
	Message string
	Code    string
	Field   string
}

func (e *Error) Error() string { return e.Message }

// Errorf returns an *Error with the given HTTP status.
func Errorf(status int, format string, args ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...)}
}

// FieldErrorf returns an *Error with the given HTTP status about the field of the request with
// the JSON key field.
func FieldErrorf(status int, field, format string, args ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...), Field: field}
}

// Within returns err about the field of the request with the JSON key field, if it is an
// *Error: one about a field of the object there gets the key in front, e.g. newServers.memoryMb,
// one about no field is about that field. Other errors stay as they are.
func Within(field string, err error) error {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return err
	}
	within := *apiErr
	within.Field = strings.TrimSuffix(field+"."+apiErr.Field, ".")
	return &within
}

// Confirm returns a conflict that the client may confirm with what code names.
func Confirm(code, format string, args ...any) error {
	return &Error{Status: http.StatusConflict, Message: fmt.Sprintf(format, args...), Code: code}
}

// grpcStatus maps errors returned by agents to HTTP statuses. Their messages are
// shown to administrators, which helps diagnosing problems on a node.
var grpcStatus = map[codes.Code]int{
	codes.InvalidArgument:    http.StatusBadRequest,
	codes.NotFound:           http.StatusNotFound,
	codes.AlreadyExists:      http.StatusConflict,
	codes.FailedPrecondition: http.StatusConflict,
	codes.PermissionDenied:   http.StatusForbidden,
	codes.ResourceExhausted:  http.StatusRequestEntityTooLarge,
	codes.Internal:           http.StatusBadGateway,
	codes.Unavailable:        http.StatusBadGateway,
	codes.DeadlineExceeded:   http.StatusGatewayTimeout,
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError writes err as {"error": "..."}, with the code and the field of an *Error if they
// are set. Unexpected errors are logged and hidden.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		WriteJSON(w, apiErr.Status, struct {
			Error string `json:"error"`
			Code  string `json:"code,omitempty"`
			Field string `json:"field,omitempty"`
		}{apiErr.Message, apiErr.Code, apiErr.Field})
		return
	}
	if st, ok := status.FromError(err); ok {
		if code, known := grpcStatus[st.Code()]; known {
			WriteJSON(w, code, map[string]string{"error": st.Message()})
			return
		}
	}
	if r.Context().Err() != nil && (errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled) {
		// The client went away, e.g. it closed a console stream: no error of the master.
		WriteJSON(w, statusClientClosed, map[string]string{"error": "canceled"})
		return
	}
	slog.Error("Request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// Message returns what users may learn about an error: the message of an *Error or of a
// gRPC status. Unexpected errors are logged and hidden.
func Message(err error) string {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	slog.Error("Unexpected error", "err", err)
	return "internal error"
}

// ReadJSON decodes a size limited request body into v and rejects unknown fields.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return ReadJSONUpTo(w, r, v, maxBodyBytes)
}

// ReadJSONUpTo is ReadJSON for bodies of up to limit bytes, e.g. with files.
func ReadJSONUpTo(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return Errorf(http.StatusBadRequest, "invalid request body: %v", err)
	}
	return nil
}
