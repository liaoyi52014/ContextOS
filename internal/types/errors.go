package types

import "fmt"

// Error code constants for AppError.
const (
	ErrUnauthorized      = "UNAUTHORIZED"
	ErrForbidden         = "FORBIDDEN"
	ErrNotFound          = "NOT_FOUND"
	ErrBadRequest        = "BAD_REQUEST"
	ErrConflict          = "CONFLICT"
	ErrInternal          = "INTERNAL"
	ErrServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// AppError is the standard application error type.
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

// Error implements the error interface.
func (e *AppError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}
