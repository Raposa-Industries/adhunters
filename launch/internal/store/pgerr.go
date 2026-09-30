package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
