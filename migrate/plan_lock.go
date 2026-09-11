package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const changePlanLockReleaseTimeout = 5 * time.Second

// ChangePlanLocker acquires the lock that serializes plan apply against one history table, named by
// Acquire's string argument. Returning a nil lock and a nil error makes plan apply fail with a
// *ChangePlanLockError whose Stage reports ChangePlanLockAcquire.
type ChangePlanLocker interface {
	Acquire(context.Context, string) (ChangePlanLock, error)
}

type ChangePlanLock interface {
	Release(context.Context) error
}

type ChangePlanLockStage string

const (
	ChangePlanLockDiscover ChangePlanLockStage = "discover"
	ChangePlanLockValidate ChangePlanLockStage = "validate"
	ChangePlanLockAcquire  ChangePlanLockStage = "acquire"
	ChangePlanLockRelease  ChangePlanLockStage = "release"
)

type ChangePlanLockError struct {
	stage ChangePlanLockStage
	cause error
}

func (e *ChangePlanLockError) Error() string {
	if e == nil {
		return "migrate: change plan lock failed"
	}
	return fmt.Sprintf("migrate: change plan lock %s: %v", e.stage, e.cause)
}
func (e *ChangePlanLockError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
func (e *ChangePlanLockError) Stage() ChangePlanLockStage {
	if e == nil {
		return ""
	}
	return e.stage
}

// WithChangePlanLocker returns a copy of r whose SQLite plan apply takes its lock by calling locker.Acquire
// with the history table name, instead of creating the ".rasql-plan-lock-" sidecar file beside the database
// file. A nil locker produces a *ChangePlanLockError whose Stage reports ChangePlanLockValidate.
//
// `locker` must not be nil.
func (r Runner) WithChangePlanLocker(locker ChangePlanLocker) (Runner, error) {
	if locker == nil {
		return Runner{}, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: errors.New("locker is nil")}
	}
	r.changePlanLocker = locker
	return r, nil
}

func (r Runner) acquireSQLiteChangePlanLock(ctx context.Context, connection *sql.Conn) (ChangePlanLock, error) {
	if r.changePlanLocker != nil {
		lock, err := r.changePlanLocker.Acquire(ctx, r.historyTable)
		if err != nil {
			return nil, &ChangePlanLockError{stage: ChangePlanLockAcquire, cause: err}
		}
		if lock == nil {
			return nil, &ChangePlanLockError{stage: ChangePlanLockAcquire, cause: errors.New("locker returned a nil lock")}
		}
		return lock, nil
	}
	filename, err := sqliteMainFilename(ctx, connection)
	if err != nil {
		return nil, &ChangePlanLockError{stage: ChangePlanLockDiscover, cause: err}
	}
	realPath, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return nil, &ChangePlanLockError{stage: ChangePlanLockDiscover, cause: err}
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return nil, &ChangePlanLockError{stage: ChangePlanLockDiscover, cause: err}
	}
	osKind, identity, err := nativePlanFileIdentity(realPath)
	if err != nil {
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: err}
	}
	suffix := framedPlanHash("rasql/changeplan/sqlite-lock/v1", []byte(osKind), identity, []byte(r.historyTable))
	path := filepath.Join(filepath.Dir(realPath), ".rasql-plan-lock-"+suffix)
	lock, err := acquireNativePlanSidecar(ctx, path)
	if err != nil {
		var typed *ChangePlanLockError
		if errors.As(err, &typed) {
			return nil, err
		}
		return nil, &ChangePlanLockError{stage: ChangePlanLockAcquire, cause: err}
	}
	return lock, nil
}

func sqliteMainFilename(ctx context.Context, connection *sql.Conn) (string, error) {
	if connection == nil {
		return "", errors.New("database connection is nil")
	}
	rows, err := connection.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	mainCount := 0
	filename := ""
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return "", err
		}
		if name == "main" {
			mainCount++
			filename = path
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if mainCount != 1 || strings.TrimSpace(filename) == "" {
		return "", errors.New("SQLite plan apply requires one persistent main database file")
	}
	return filename, nil
}

func releaseChangePlanLock(lock ChangePlanLock, operationErr error) error {
	if lock == nil {
		return operationErr
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), changePlanLockReleaseTimeout)
	defer cancel()
	if err := lock.Release(cleanupCtx); err != nil {
		return errors.Join(operationErr, &ChangePlanLockError{stage: ChangePlanLockRelease, cause: err})
	}
	return operationErr
}

func framedPlanHash(domain string, components ...[]byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	var size [8]byte
	for _, component := range components {
		binary.BigEndian.PutUint64(size[:], uint64(len(component)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(component)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
