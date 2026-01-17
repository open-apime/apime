package postgres

import "errors"

var ErrNotFound = errors.New("not found")

var ErrLastAdmin = errors.New("cannot delete the last admin user")
