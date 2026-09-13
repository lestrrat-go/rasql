package migrate

import (
	"fmt"
	"io"

	"github.com/lestrrat-go/rasql/internal/mysqlerrno"
)

// The notice a tolerated statement writes. It carries the migration, the
// source file, and the engine's own error text, and it names the same nouns
// the failure message names, so the two read alike.
const (
	appliedNoticeFormat  = "migrate: warning: migration %q SQL source %q was already applied: %v\n"
	revertedNoticeFormat = "migrate: warning: migration %q reverse SQL source %q was already reverted: %v\n"
)

// toleratedAsApplied reports whether err means the forward source that raised
// it asked MySQL for work the database had already done, and writes one
// notice line when it does.
func (r Runner) toleratedAsApplied(migrationID string, source string, err error) bool {
	return r.tolerateAlreadyDone(appliedNoticeFormat, migrationID, source, err)
}

// toleratedAsReverted is toleratedAsApplied for a reverse source.
func (r Runner) toleratedAsReverted(migrationID string, source string, err error) bool {
	return r.tolerateAlreadyDone(revertedNoticeFormat, migrationID, source, err)
}

// tolerateAlreadyDone is MySQL-only, and deliberately so. SQLite runs a whole
// migration inside one transaction, so a failed SQLite migration leaves
// nothing behind to tolerate. PostgreSQL accepts IF NOT EXISTS or IF EXISTS on
// nearly every DDL form, so a PostgreSQL source can be spelled so that
// re-running it is not an error in the first place. MySQL 8.4 accepts that
// spelling only after CREATE TABLE, DROP TABLE, CREATE VIEW, and DROP VIEW,
// and commits DDL implicitly whatever mode a migration declares, so it is the
// one engine that both keeps the work a failed migration already did and
// offers no spelling that survives the retry. See internal/mysqlerrno for the
// numbers and for what each one means.
func (r Runner) tolerateAlreadyDone(noticeFormat string, migrationID string, source string, err error) bool {
	if r.dialect == nil || r.dialect.Name() != "mysql" {
		return false
	}
	number, ok := mysqlerrno.Number(err)
	if !ok || !mysqlerrno.AlreadyApplied(number) {
		return false
	}
	notices := r.notices
	if notices == nil {
		notices = io.Discard
	}
	_, _ = fmt.Fprintf(notices, noticeFormat, migrationID, source, err)
	return true
}
