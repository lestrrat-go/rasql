//go:build unix

package inspect_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestCreateRoleErrorProvesNothingCreated pins createRoleErrorProvesNothingCreated's
// classification of PostgreSQL error codes: this is the guard
// createRestrictedRoleFlow checks before deciding a CREATE ROLE failure
// proves nothing was created. Getting a code wrong here either leaks a
// cluster-wide role (a false negative -- the code proves nothing created,
// but this reports false) or drops a role this call never created (a false
// positive -- some other code is wrongly treated as proof).
func TestCreateRoleErrorProvesNothingCreated(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "duplicate_object (42710): CREATE ROLE's own pre-check found the name already exists",
			err:  &pgconn.PgError{Code: pgDuplicateObject},
			want: true,
		},
		{
			name: "unique_violation (23505): the accepted race between the probe and a concurrent CREATE ROLE",
			err:  &pgconn.PgError{Code: pgUniqueViolation},
			want: true,
		},
		{
			name: "insufficient_privilege (42501): admin lacks CREATEROLE, so the statement was rejected before creating anything",
			err:  &pgconn.PgError{Code: pgInsufficientPrivilege},
			want: true,
		},
		{
			name: "an unrelated PostgreSQL error code proves nothing: the role may have been created despite it",
			err:  &pgconn.PgError{Code: "08006"}, // connection_failure
			want: false,
		},
		{
			name: "an error carrying no PostgreSQL code at all (e.g. context cancellation) proves nothing",
			err:  errors.New("context canceled"),
			want: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := createRoleErrorProvesNothingCreated(test.err)
			if got != test.want {
				t.Fatalf("createRoleErrorProvesNothingCreated(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

// TestCreateRestrictedRoleFlow pins the containment ordering
// createRestrictedRoleFlow implements (see its own doc for the three
// steps), using fake probeExists/create/drop functions instead of a live
// server -- this package cannot open a PostgreSQL connection in this
// environment, and even where it can, a database-backed test could only
// prove the happy path, not the specific error-code branches this exists to
// get right.
//
// Every subtest asserts on the *fake* drop function actually being invoked
// or not, never merely on the returned error: the property under test is
// containment -- whether a drop is attempted -- not whether the call
// failed, and a test that only checked the error would pass even if the
// dropOwned guard were deleted entirely.
func TestCreateRestrictedRoleFlow(t *testing.T) {
	t.Run("pre-existing name fails loudly with no drop ever registered", func(t *testing.T) {
		probeCalls, createCalls, dropCalls := 0, 0, 0
		cleanup, err := createRestrictedRoleFlow(
			"exists",
			func() (bool, error) { probeCalls++; return true, nil },
			func() error { createCalls++; return nil },
			func(error) bool {
				t.Fatal("errorProvesNothingCreated must not be called: create must never run for a pre-existing name")
				return false
			},
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil error for a pre-existing name, want an error")
		}
		if cleanup != nil {
			t.Fatal("createRestrictedRoleFlow returned a non-nil cleanup for a pre-existing name; nothing may ever be registered for a name this call refused to touch")
		}
		if probeCalls != 1 {
			t.Fatalf("probeExists called %d times, want exactly 1", probeCalls)
		}
		if createCalls != 0 {
			t.Fatalf("create called %d times, want 0: CREATE ROLE must never run for a name the probe found pre-existing", createCalls)
		}
		if dropCalls != 0 {
			t.Fatalf("drop called %d times, want 0: cleanup is nil, so nothing could have invoked it -- a pre-existing role must never be dropped by this harness", dropCalls)
		}
	})

	t.Run("name-collision create error clears the flag so the drop does not run", func(t *testing.T) {
		dropCalls := 0
		cleanup, err := createRestrictedRoleFlow(
			"dup",
			func() (bool, error) { return false, nil },
			func() error { return &pgconn.PgError{Code: pgDuplicateObject} },
			createRoleErrorProvesNothingCreated,
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil error for a failed create, want an error")
		}
		if cleanup == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil cleanup for a failed create; a cleanup must still be registered so ambiguous errors are not silently leaked")
		}
		cleanup()
		if dropCalls != 0 {
			t.Fatalf("drop called %d times after cleanup(), want 0: a proven name-collision error must never trigger a drop of a role this call did not create", dropCalls)
		}
	})

	t.Run("insufficient-privilege create error clears the flag so the drop does not run", func(t *testing.T) {
		dropCalls := 0
		cleanup, err := createRestrictedRoleFlow(
			"priv",
			func() (bool, error) { return false, nil },
			func() error { return &pgconn.PgError{Code: pgInsufficientPrivilege} },
			createRoleErrorProvesNothingCreated,
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil error for a failed create, want an error")
		}
		cleanup()
		if dropCalls != 0 {
			t.Fatalf("drop called %d times after cleanup(), want 0: a proven insufficient-privilege error must never trigger a drop", dropCalls)
		}
	})

	t.Run("create error with no proof leaves the flag set so the drop still runs", func(t *testing.T) {
		dropCalls := 0
		cleanup, err := createRestrictedRoleFlow(
			"cancel",
			func() (bool, error) { return false, nil },
			func() error { return errors.New("connection reset") }, // an error carrying no driver-specific code at all
			createRoleErrorProvesNothingCreated,
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil error for a failed create, want an error")
		}
		if cleanup == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil cleanup for a failed create, want a cleanup so the leak stays closed")
		}
		cleanup()
		if dropCalls != 1 {
			t.Fatalf("drop called %d times after cleanup(), want exactly 1: an unproven error (e.g. a connection reset) must still attempt the drop, since the role may have been created despite the client-visible error -- a lost CREATE ROLE response is exactly this case", dropCalls)
		}
	})

	t.Run("happy path creates once and the returned cleanup drops exactly once", func(t *testing.T) {
		probeCalls, createCalls, dropCalls := 0, 0, 0
		cleanup, err := createRestrictedRoleFlow(
			"ok",
			func() (bool, error) { probeCalls++; return false, nil },
			func() error { createCalls++; return nil },
			func(error) bool {
				t.Fatal("errorProvesNothingCreated must not be called: create succeeded")
				return false
			},
			func() { dropCalls++ },
		)
		if err != nil {
			t.Fatalf("createRestrictedRoleFlow returned an error for a successful create: %v", err)
		}
		if probeCalls != 1 || createCalls != 1 {
			t.Fatalf("probeExists called %d times and create called %d times, want exactly 1 each", probeCalls, createCalls)
		}
		if cleanup == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil cleanup for a successful create, want a cleanup that drops the role it created")
		}
		cleanup()
		if dropCalls != 1 {
			t.Fatalf("drop called %d times after cleanup(), want exactly 1", dropCalls)
		}
	})

	t.Run("a lost response after a successful create still drops the role", func(t *testing.T) {
		// This is the leak createRestrictedRoleFlow's registration ordering
		// exists to close: the server commits CREATE ROLE, but the client
		// never sees a reply (e.g. the connection drops right after). The
		// error create returns here carries no PostgreSQL code at all --
		// exactly what a read timeout or connection reset looks like -- so
		// errorProvesNothingCreated must report false and the drop must
		// still run.
		dropCalls := 0
		cleanup, err := createRestrictedRoleFlow(
			"lost-response",
			func() (bool, error) { return false, nil },
			func() error { return errors.New("read tcp: i/o timeout") },
			createRoleErrorProvesNothingCreated,
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil error for a failed create, want an error")
		}
		if cleanup == nil {
			t.Fatal("createRestrictedRoleFlow returned a nil cleanup for a lost-response error, want a cleanup so the leak stays closed")
		}
		cleanup()
		if dropCalls != 1 {
			t.Fatalf("drop called %d times after cleanup(), want exactly 1: a lost response must still attempt the drop, since CREATE ROLE may have committed on the server despite the client never seeing the reply", dropCalls)
		}
	})
}

// TestDropRestrictedRoleSteps pins that dropRestrictedRoleSteps attempts
// both cleanup statements regardless of an earlier one's failure, and
// reports every failure rather than swallowing one. Before this fix, the
// first statement (DROP OWNED BY) used a FailNow-style assertion: a failure
// there stopped the cleanup entirely, so DROP ROLE never ran and the
// cluster-wide role leaked.
func TestDropRestrictedRoleSteps(t *testing.T) {
	t.Run("a failure in the first step does not prevent the second from running, and both are reported", func(t *testing.T) {
		var reports []string
		dropOwnedCalls, dropRoleCalls := 0, 0
		dropRestrictedRoleSteps(
			func(msg string) { reports = append(reports, msg) },
			func() error { dropOwnedCalls++; return errors.New("drop owned by: boom") },
			func() error { dropRoleCalls++; return errors.New("drop role: boom") },
		)
		if dropOwnedCalls != 1 {
			t.Fatalf("dropOwned called %d times, want exactly 1", dropOwnedCalls)
		}
		if dropRoleCalls != 1 {
			t.Fatalf("dropRole called %d times, want exactly 1: a failure in the first step must not prevent the second from running", dropRoleCalls)
		}
		if len(reports) != 2 {
			t.Fatalf("got %d reported failures, want 2: both steps' failures must be reported, not just the first", len(reports))
		}
	})

	t.Run("a failure only in the second step is still reported", func(t *testing.T) {
		var reports []string
		dropRestrictedRoleSteps(
			func(msg string) { reports = append(reports, msg) },
			func() error { return nil },
			func() error { return errors.New("drop role: boom") },
		)
		if len(reports) != 1 {
			t.Fatalf("got %d reported failures, want exactly 1", len(reports))
		}
	})

	t.Run("happy path runs both steps and reports nothing", func(t *testing.T) {
		var reports []string
		dropOwnedCalls, dropRoleCalls := 0, 0
		dropRestrictedRoleSteps(
			func(msg string) { reports = append(reports, msg) },
			func() error { dropOwnedCalls++; return nil },
			func() error { dropRoleCalls++; return nil },
		)
		if dropOwnedCalls != 1 || dropRoleCalls != 1 {
			t.Fatalf("dropOwned called %d times, dropRole called %d times, want exactly 1 each", dropOwnedCalls, dropRoleCalls)
		}
		if len(reports) != 0 {
			t.Fatalf("got %d reported failures for a successful cleanup, want 0", len(reports))
		}
	})
}
