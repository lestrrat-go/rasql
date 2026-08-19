// Package dsnredact keeps a connection string out of an error message.
//
// Every rasql command that takes -dsn passes the driver's own errors through
// Error before reporting them. A driver is free to quote the connection
// string it was handed, and that string carries a password, so an unredacted
// failure prints a credential into a terminal, a CI log, or an issue report.
package dsnredact

import "strings"

// Error returns err with every occurrence of dsn in its message replaced by
// "[redacted]". A nil err, or an empty dsn, is returned unchanged.
//
// The result wraps err, so errors.Is and errors.As still reach whatever the
// driver returned. Only the message is rewritten.
func Error(err error, dsn string) error {
	if err == nil || dsn == "" {
		return err
	}
	return redactedError{
		message: strings.ReplaceAll(err.Error(), dsn, "[redacted]"),
		cause:   err,
	}
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string {
	return e.message
}

func (e redactedError) Unwrap() error {
	return e.cause
}
