package sqlite

import (
	"database/sql"
	"errors"
)

var ErrNotFound = errors.New("not found")

var ErrLastAdmin = errors.New("cannot delete the last admin user")

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
