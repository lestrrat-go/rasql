package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

const postgreSQLLockReleaseTimeout = 5 * time.Second

func (r Runner) withPostgreSQLLock(ctx context.Context, connection *sql.Conn, run func() ([]Migration, error)) ([]Migration, error) {
	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", r.historySQL); err != nil {
		return nil, fmt.Errorf("migrate: acquire PostgreSQL migration lock: %w", err)
	}
	completed, operationErr := run()
	cleanupErr := r.releasePostgreSQLLock(connection)
	return completed, errors.Join(operationErr, cleanupErr)
}

func (r Runner) withPostgreSQLReadLock(ctx context.Context, connection *sql.Conn, run func() error) error {
	_, err := r.withPostgreSQLLock(ctx, connection, func() ([]Migration, error) { return nil, run() })
	return err
}

func (r Runner) releasePostgreSQLLock(connection *sql.Conn) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), postgreSQLLockReleaseTimeout)
	defer cancel()
	var released bool
	err := connection.QueryRowContext(cleanupCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", r.historySQL).Scan(&released)
	if err == nil && released {
		return nil
	}
	var releaseErr error
	if err != nil {
		releaseErr = fmt.Errorf("migrate: release PostgreSQL migration lock: %w", err)
	} else {
		releaseErr = errors.New("migrate: release PostgreSQL migration lock: lock was not held")
	}
	markBadErr := connection.Raw(func(any) error { return driver.ErrBadConn })
	if markBadErr != nil {
		if errors.Is(markBadErr, driver.ErrBadConn) {
			return errors.Join(releaseErr, fmt.Errorf("migrate: marked PostgreSQL migration connection bad: %w", markBadErr))
		}
		return errors.Join(releaseErr, fmt.Errorf("migrate: could not mark PostgreSQL migration connection bad: %w", markBadErr))
	}
	return fmt.Errorf("%w; PostgreSQL migration connection marked bad", releaseErr)
}
