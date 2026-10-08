package store

import "errors"

var (
	ErrNotFound       = errors.New("record not found")
	ErrDuplicate      = errors.New("duplicate record")
	ErrCourseNotFound = errors.New("course not found")
)
