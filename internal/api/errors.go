package api

import "errors"

// Category classifies an operation failure so adapters can map it to a
// transport response without inspecting internal causes.
type Category int

const (
	CategoryInvalidInput Category = iota
	CategoryUnauthenticated
	CategoryForbidden
	CategoryNotFound
	CategoryConflict
	CategoryBusy
	CategoryUnavailable
)

// String returns the stable category name used in logs.
func (c Category) String() string {
	switch c {
	case CategoryInvalidInput:
		return "invalid input"
	case CategoryUnauthenticated:
		return "unauthenticated"
	case CategoryForbidden:
		return "forbidden"
	case CategoryNotFound:
		return "not found"
	case CategoryConflict:
		return "conflict"
	case CategoryBusy:
		return "busy"
	case CategoryUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// Error is a categorised operation failure. Message is safe for a client;
// cause is the raw internal error, kept for server logs and never serialised.
type Error struct {
	Category Category
	Message  string
	cause    error
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// Unwrap exposes the internal cause to errors.Is/errors.As and logs only.
func (e *Error) Unwrap() error { return e.cause }

// Cause returns the raw internal error, or nil.
func (e *Error) Cause() error { return e.cause }

// InvalidInput reports malformed or rejected caller input.
func InvalidInput(message string, cause error) *Error {
	return &Error{Category: CategoryInvalidInput, Message: message, cause: cause}
}

// Unauthenticated reports a missing or unusable identity.
func Unauthenticated(message string) *Error {
	return &Error{Category: CategoryUnauthenticated, Message: message}
}

// Forbidden reports an identity without the required permission.
func Forbidden(message string) *Error {
	return &Error{Category: CategoryForbidden, Message: message}
}

// NotFound reports a missing resource.
func NotFound(message string) *Error {
	return &Error{Category: CategoryNotFound, Message: message}
}

// Conflict reports a rejected optimistic write.
func Conflict(message string) *Error {
	return &Error{Category: CategoryConflict, Message: message}
}

// NotFoundCause reports a missing resource while retaining the underlying error
// for server logs.
func NotFoundCause(message string, cause error) *Error {
	return &Error{Category: CategoryNotFound, Message: message, cause: cause}
}

// Busy reports a temporarily contended operation.
func Busy(message string) *Error {
	return &Error{Category: CategoryBusy, Message: message}
}

// Unavailable reports a dependency or internal failure.
func Unavailable(message string, cause error) *Error {
	return &Error{Category: CategoryUnavailable, Message: message, cause: cause}
}

// CategoryOf returns the category of err, defaulting to CategoryUnavailable for
// an uncategorised error.
func CategoryOf(err error) Category {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Category
	}
	return CategoryUnavailable
}
