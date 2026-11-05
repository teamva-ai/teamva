package domain

import "fmt"

type Error struct {
	Code       string
	Message    string
	Retryable  bool
	HTTPStatus int
	Details    map[string]any
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func NotFound(kind, id string) *Error {
	return &Error{
		Code:       "not_found",
		Message:    kind + " 不存在",
		HTTPStatus: 404,
		Details:    map[string]any{"id": id},
	}
}

func Invalid(message string) *Error {
	return &Error{Code: "invalid_argument", Message: message, HTTPStatus: 400}
}

func Conflict(code, message string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: 409, Details: details}
}

func Unauthorized(code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: 401}
}

func Forbidden(code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: 403}
}
