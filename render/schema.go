package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/statement"
)

// ErrUnsupportedIndexMethod is the sentinel wrapped by every
// [UnsupportedIndexMethodError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexMethod = errors.New("render: unsupported index method")

// UnsupportedIndexMethodError reports that an IndexDef names a non-default
// [schema.IndexMethod]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for anything other than a plain default index.
type UnsupportedIndexMethodError struct {
	// Index is the name of the index that named a non-default method.
	Index string
	// Method is the non-default method the index named.
	Method schema.IndexMethod
}

func (e *UnsupportedIndexMethodError) Error() string {
	return fmt.Sprintf("index %q uses method %q, which rasql can describe but not yet render", e.Index, e.Method)
}

// Unwrap exposes ErrUnsupportedIndexMethod so
// errors.Is(err, ErrUnsupportedIndexMethod) works alongside errors.As
// against *UnsupportedIndexMethodError.
func (e *UnsupportedIndexMethodError) Unwrap() error {
	return ErrUnsupportedIndexMethod
}

// ErrUnsupportedPartialIndex is the sentinel wrapped by every
// [UnsupportedPartialIndexError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedPartialIndex = errors.New("render: unsupported partial index")

// UnsupportedPartialIndexError reports that an IndexDef names a
// [schema.IndexDef.Predicate], making it a partial index, on a dialect
// that has no [dialect.CapabilityPartialIndex]: MySQL, and any other
// dialect that does not grant the capability. inspect can describe such an
// index on any dialect, and TableDef.Validate accepts it, but this
// package cannot express a WHERE clause on an index for a dialect lacking
// the capability.
type UnsupportedPartialIndexError struct {
	// Index is the name of the index that named a predicate.
	Index string
	// Predicate is the WHERE-clause expression text the index named.
	Predicate string
	// Dialect is the name of the dialect that cannot express a partial
	// index.
	Dialect string
}

func (e *UnsupportedPartialIndexError) Error() string {
	return fmt.Sprintf("index %q has predicate %q, which the %s dialect cannot express: it has no partial index feature", e.Index, e.Predicate, e.Dialect)
}

// Unwrap exposes ErrUnsupportedPartialIndex so
// errors.Is(err, ErrUnsupportedPartialIndex) works alongside errors.As
// against *UnsupportedPartialIndexError.
func (e *UnsupportedPartialIndexError) Unwrap() error {
	return ErrUnsupportedPartialIndex
}

// ErrUnsupportedExpressionIndex is the sentinel wrapped by every
// [UnsupportedExpressionIndexError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedExpressionIndex = errors.New("render: unsupported expression index")

// UnsupportedExpressionIndexError reports that an IndexDef names
// [schema.IndexDef.Expressions], meaning at least one of its keys is not a
// plain column reference. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for an expression key.
type UnsupportedExpressionIndexError struct {
	// Index is the name of the index that named expression keys.
	Index string
	// Expressions is the ordered list of key expressions the index named.
	Expressions []string
}

func (e *UnsupportedExpressionIndexError) Error() string {
	return fmt.Sprintf("index %q has expression keys %q, which rasql can describe but not yet render", e.Index, e.Expressions)
}

// Unwrap exposes ErrUnsupportedExpressionIndex so
// errors.Is(err, ErrUnsupportedExpressionIndex) works alongside errors.As
// against *UnsupportedExpressionIndexError.
func (e *UnsupportedExpressionIndexError) Unwrap() error {
	return ErrUnsupportedExpressionIndex
}

// ErrUnsupportedIndexIncludeColumns is the sentinel wrapped by every
// [UnsupportedIndexIncludeColumnsError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexIncludeColumns = errors.New("render: unsupported index include columns")

// UnsupportedIndexIncludeColumnsError reports that an IndexDef names
// [schema.IndexDef.IncludeColumns]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for an INCLUDE clause.
type UnsupportedIndexIncludeColumnsError struct {
	// Index is the name of the index that named INCLUDE columns.
	Index string
	// IncludeColumns is the ordered list of included column names the
	// index named.
	IncludeColumns []string
}

func (e *UnsupportedIndexIncludeColumnsError) Error() string {
	return fmt.Sprintf("index %q includes columns %q, which rasql can describe but not yet render", e.Index, e.IncludeColumns)
}

// Unwrap exposes ErrUnsupportedIndexIncludeColumns so
// errors.Is(err, ErrUnsupportedIndexIncludeColumns) works alongside
// errors.As against *UnsupportedIndexIncludeColumnsError.
func (e *UnsupportedIndexIncludeColumnsError) Unwrap() error {
	return ErrUnsupportedIndexIncludeColumns
}

// ErrUnsupportedIndexInvisible is the sentinel wrapped by every
// [UnsupportedIndexInvisibleError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexInvisible = errors.New("render: unsupported invisible index")

// UnsupportedIndexInvisibleError reports that an IndexDef sets
// [schema.IndexDef.Invisible]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for an INVISIBLE index.
type UnsupportedIndexInvisibleError struct {
	// Index is the name of the index marked invisible.
	Index string
}

func (e *UnsupportedIndexInvisibleError) Error() string {
	return fmt.Sprintf("index %q is invisible, which rasql can describe but not yet render", e.Index)
}

// Unwrap exposes ErrUnsupportedIndexInvisible so
// errors.Is(err, ErrUnsupportedIndexInvisible) works alongside errors.As
// against *UnsupportedIndexInvisibleError.
func (e *UnsupportedIndexInvisibleError) Unwrap() error {
	return ErrUnsupportedIndexInvisible
}

// ErrUnsupportedIndexKeyDetails is the sentinel wrapped by every
// [UnsupportedIndexKeyDetailsError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexKeyDetails = errors.New("render: unsupported index key details")

// UnsupportedIndexKeyDetailsError reports that an IndexDef names
// [schema.IndexDef.Keys], meaning at least one of its keys carries a
// per-key fact — DESC order, a non-default collation or operator class, or
// a MySQL prefix length — beyond a plain ascending expression.
// inspect can describe such an index, and TableDef.Validate accepts it,
// but this package does not yet know how to build DDL for any of those
// per-key facts.
type UnsupportedIndexKeyDetailsError struct {
	// Index is the name of the index that named per-key details.
	Index string
	// Keys is the ordered list of per-key facts the index named.
	Keys []schema.IndexKeyDef
}

func (e *UnsupportedIndexKeyDetailsError) Error() string {
	return fmt.Sprintf("index %q has per-key details %+v, which rasql can describe but not yet render", e.Index, e.Keys)
}

// Unwrap exposes ErrUnsupportedIndexKeyDetails so
// errors.Is(err, ErrUnsupportedIndexKeyDetails) works alongside errors.As
// against *UnsupportedIndexKeyDetailsError.
func (e *UnsupportedIndexKeyDetailsError) Unwrap() error {
	return ErrUnsupportedIndexKeyDetails
}

// ErrUnsupportedIndexNotValid is the sentinel wrapped by every
// [UnsupportedIndexNotValidError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexNotValid = errors.New("render: unsupported invalid index")

// UnsupportedIndexNotValidError reports that an IndexDef sets
// [schema.IndexDef.NotValid]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for an invalid index.
type UnsupportedIndexNotValidError struct {
	// Index is the name of the index marked invalid.
	Index string
}

func (e *UnsupportedIndexNotValidError) Error() string {
	return fmt.Sprintf("index %q is not valid, which rasql can describe but not yet render", e.Index)
}

// Unwrap exposes ErrUnsupportedIndexNotValid so
// errors.Is(err, ErrUnsupportedIndexNotValid) works alongside errors.As
// against *UnsupportedIndexNotValidError.
func (e *UnsupportedIndexNotValidError) Unwrap() error {
	return ErrUnsupportedIndexNotValid
}

// ErrUnsupportedIndexStorageParameters is the sentinel wrapped by every
// [UnsupportedIndexStorageParametersError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexStorageParameters = errors.New("render: unsupported index storage parameters")

// UnsupportedIndexStorageParametersError reports that an IndexDef names
// [schema.IndexDef.StorageParameters]. inspect can describe such an index,
// and TableDef.Validate accepts it, but this package does not yet know how
// to build DDL for a WITH (...) storage-parameters clause.
type UnsupportedIndexStorageParametersError struct {
	// Index is the name of the index that named storage parameters.
	Index string
	// StorageParameters is the storage parameters the index named.
	StorageParameters map[string]string
}

func (e *UnsupportedIndexStorageParametersError) Error() string {
	return fmt.Sprintf("index %q has storage parameters %v, which rasql can describe but not yet render", e.Index, e.StorageParameters)
}

// Unwrap exposes ErrUnsupportedIndexStorageParameters so
// errors.Is(err, ErrUnsupportedIndexStorageParameters) works alongside
// errors.As against *UnsupportedIndexStorageParametersError.
func (e *UnsupportedIndexStorageParametersError) Unwrap() error {
	return ErrUnsupportedIndexStorageParameters
}

// ErrUnsupportedIndexTablespace is the sentinel wrapped by every
// [UnsupportedIndexTablespaceError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexTablespace = errors.New("render: unsupported index tablespace")

// UnsupportedIndexTablespaceError reports that an IndexDef names a
// [schema.IndexDef.Tablespace]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for a TABLESPACE clause.
type UnsupportedIndexTablespaceError struct {
	// Index is the name of the index that named a tablespace.
	Index string
	// Tablespace is the tablespace the index named.
	Tablespace string
}

func (e *UnsupportedIndexTablespaceError) Error() string {
	return fmt.Sprintf("index %q uses tablespace %q, which rasql can describe but not yet render", e.Index, e.Tablespace)
}

// Unwrap exposes ErrUnsupportedIndexTablespace so
// errors.Is(err, ErrUnsupportedIndexTablespace) works alongside errors.As
// against *UnsupportedIndexTablespaceError.
func (e *UnsupportedIndexTablespaceError) Unwrap() error {
	return ErrUnsupportedIndexTablespace
}

// ErrUnsupportedIndexReplicaIdentity is the sentinel wrapped by every
// [UnsupportedIndexReplicaIdentityError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexReplicaIdentity = errors.New("render: unsupported index replica identity")

// UnsupportedIndexReplicaIdentityError reports that an IndexDef sets
// [schema.IndexDef.ReplicaIdentity]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for a REPLICA IDENTITY USING INDEX declaration.
type UnsupportedIndexReplicaIdentityError struct {
	// Index is the name of the index marked as the replica identity.
	Index string
}

func (e *UnsupportedIndexReplicaIdentityError) Error() string {
	return fmt.Sprintf("index %q is the replica identity, which rasql can describe but not yet render", e.Index)
}

// Unwrap exposes ErrUnsupportedIndexReplicaIdentity so
// errors.Is(err, ErrUnsupportedIndexReplicaIdentity) works alongside
// errors.As against *UnsupportedIndexReplicaIdentityError.
func (e *UnsupportedIndexReplicaIdentityError) Unwrap() error {
	return ErrUnsupportedIndexReplicaIdentity
}

// ErrUnsupportedIndexNullsNotDistinct is the sentinel wrapped by every
// [UnsupportedIndexNullsNotDistinctError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexNullsNotDistinct = errors.New("render: unsupported index nulls-not-distinct")

// UnsupportedIndexNullsNotDistinctError reports that an IndexDef sets
// [schema.IndexDef.NullsNotDistinct]. inspect can describe such a plain
// unique index, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a NULLS NOT DISTINCT clause.
type UnsupportedIndexNullsNotDistinctError struct {
	// Index is the name of the index that set NullsNotDistinct.
	Index string
}

func (e *UnsupportedIndexNullsNotDistinctError) Error() string {
	return fmt.Sprintf("index %q uses NULLS NOT DISTINCT, which rasql can describe but not yet render", e.Index)
}

// Unwrap exposes ErrUnsupportedIndexNullsNotDistinct so
// errors.Is(err, ErrUnsupportedIndexNullsNotDistinct) works alongside
// errors.As against *UnsupportedIndexNullsNotDistinctError.
func (e *UnsupportedIndexNullsNotDistinctError) Unwrap() error {
	return ErrUnsupportedIndexNullsNotDistinct
}

// ErrUnsupportedForeignKeyMatch is the sentinel wrapped by every
// [UnsupportedForeignKeyMatchError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedForeignKeyMatch = errors.New("render: unsupported foreign key match type")

// UnsupportedForeignKeyMatchError reports that a ForeignKeyDef names a
// non-default [schema.MatchType]. inspect can describe such a foreign key,
// and TableDef.Validate accepts it, but this package does not yet know how
// to build DDL for anything other than a plain MATCH SIMPLE foreign key.
type UnsupportedForeignKeyMatchError struct {
	// ForeignKey is the name of the foreign key that named a non-default
	// match type.
	ForeignKey string
	// Match is the non-default match type the foreign key named.
	Match schema.MatchType
}

func (e *UnsupportedForeignKeyMatchError) Error() string {
	return fmt.Sprintf("foreign key %q uses MATCH %s, which rasql can describe but not yet render", e.ForeignKey, e.Match)
}

// Unwrap exposes ErrUnsupportedForeignKeyMatch so
// errors.Is(err, ErrUnsupportedForeignKeyMatch) works alongside errors.As
// against *UnsupportedForeignKeyMatchError.
func (e *UnsupportedForeignKeyMatchError) Unwrap() error {
	return ErrUnsupportedForeignKeyMatch
}

// ErrUnsupportedForeignKeyDeferrability is the sentinel wrapped by every
// [UnsupportedForeignKeyDeferrabilityError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedForeignKeyDeferrability = errors.New("render: unsupported foreign key deferrability")

// UnsupportedForeignKeyDeferrabilityError reports that a ForeignKeyDef
// names a non-default [schema.Deferrability]. inspect can describe such a
// foreign key, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for anything other than a plain NOT DEFERRABLE
// foreign key.
type UnsupportedForeignKeyDeferrabilityError struct {
	// ForeignKey is the name of the foreign key that named a non-default
	// deferrability.
	ForeignKey string
	// Deferrable is the non-default deferrability the foreign key named.
	Deferrable schema.Deferrability
}

func (e *UnsupportedForeignKeyDeferrabilityError) Error() string {
	return fmt.Sprintf("foreign key %q is %s, which rasql can describe but not yet render", e.ForeignKey, e.Deferrable)
}

// Unwrap exposes ErrUnsupportedForeignKeyDeferrability so
// errors.Is(err, ErrUnsupportedForeignKeyDeferrability) works alongside
// errors.As against *UnsupportedForeignKeyDeferrabilityError.
func (e *UnsupportedForeignKeyDeferrabilityError) Unwrap() error {
	return ErrUnsupportedForeignKeyDeferrability
}

// ErrUnsupportedCheckNoInherit is the sentinel wrapped by every
// [UnsupportedCheckNoInheritError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedCheckNoInherit = errors.New("render: unsupported check constraint NO INHERIT")

// UnsupportedCheckNoInheritError reports that a CheckDef names
// [schema.CheckDef.NoInherit]. inspect can describe such a check
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a NO INHERIT check constraint.
type UnsupportedCheckNoInheritError struct {
	// Check is the name of the check constraint declared NO INHERIT.
	Check string
}

func (e *UnsupportedCheckNoInheritError) Error() string {
	return fmt.Sprintf("check constraint %q is NO INHERIT, which rasql can describe but not yet render", e.Check)
}

// Unwrap exposes ErrUnsupportedCheckNoInherit so
// errors.Is(err, ErrUnsupportedCheckNoInherit) works alongside errors.As
// against *UnsupportedCheckNoInheritError.
func (e *UnsupportedCheckNoInheritError) Unwrap() error {
	return ErrUnsupportedCheckNoInherit
}

// ErrUnsupportedCheckNotValid is the sentinel wrapped by every
// [UnsupportedCheckNotValidError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedCheckNotValid = errors.New("render: unsupported check constraint NOT VALID")

// UnsupportedCheckNotValidError reports that a CheckDef names
// [schema.CheckDef.NotValid]. inspect can describe such a check constraint,
// and TableDef.Validate accepts it, but this package does not yet know how
// to build DDL for a NOT VALID check constraint.
type UnsupportedCheckNotValidError struct {
	// Check is the name of the check constraint declared NOT VALID.
	Check string
}

func (e *UnsupportedCheckNotValidError) Error() string {
	return fmt.Sprintf("check constraint %q is NOT VALID, which rasql can describe but not yet render", e.Check)
}

// Unwrap exposes ErrUnsupportedCheckNotValid so
// errors.Is(err, ErrUnsupportedCheckNotValid) works alongside errors.As
// against *UnsupportedCheckNotValidError.
func (e *UnsupportedCheckNotValidError) Unwrap() error {
	return ErrUnsupportedCheckNotValid
}

// ErrUnsupportedCheckNotEnforced is the sentinel wrapped by every
// [UnsupportedCheckNotEnforcedError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedCheckNotEnforced = errors.New("render: unsupported check constraint NOT ENFORCED")

// UnsupportedCheckNotEnforcedError reports that a CheckDef names
// [schema.CheckDef.NotEnforced]. inspect can describe such a check
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a NOT ENFORCED check constraint.
type UnsupportedCheckNotEnforcedError struct {
	// Check is the name of the check constraint declared NOT ENFORCED.
	Check string
}

func (e *UnsupportedCheckNotEnforcedError) Error() string {
	return fmt.Sprintf("check constraint %q is NOT ENFORCED, which rasql can describe but not yet render", e.Check)
}

// Unwrap exposes ErrUnsupportedCheckNotEnforced so
// errors.Is(err, ErrUnsupportedCheckNotEnforced) works alongside errors.As
// against *UnsupportedCheckNotEnforcedError.
func (e *UnsupportedCheckNotEnforcedError) Unwrap() error {
	return ErrUnsupportedCheckNotEnforced
}

// ErrUnsupportedForeignKeyNotValid is the sentinel wrapped by every
// [UnsupportedForeignKeyNotValidError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedForeignKeyNotValid = errors.New("render: unsupported foreign key NOT VALID")

// UnsupportedForeignKeyNotValidError reports that a ForeignKeyDef names
// [schema.ForeignKeyDef.NotValid]. inspect can describe such a foreign key,
// and TableDef.Validate accepts it, but this package does not yet know how
// to build DDL for a NOT VALID foreign key.
type UnsupportedForeignKeyNotValidError struct {
	// ForeignKey is the name of the foreign key declared NOT VALID.
	ForeignKey string
}

func (e *UnsupportedForeignKeyNotValidError) Error() string {
	return fmt.Sprintf("foreign key %q is NOT VALID, which rasql can describe but not yet render", e.ForeignKey)
}

// Unwrap exposes ErrUnsupportedForeignKeyNotValid so
// errors.Is(err, ErrUnsupportedForeignKeyNotValid) works alongside
// errors.As against *UnsupportedForeignKeyNotValidError.
func (e *UnsupportedForeignKeyNotValidError) Unwrap() error {
	return ErrUnsupportedForeignKeyNotValid
}

// ErrUnsupportedForeignKeyNotEnforced is the sentinel wrapped by every
// [UnsupportedForeignKeyNotEnforcedError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedForeignKeyNotEnforced = errors.New("render: unsupported foreign key NOT ENFORCED")

// UnsupportedForeignKeyNotEnforcedError reports that a ForeignKeyDef names
// [schema.ForeignKeyDef.NotEnforced]. inspect can describe such a foreign
// key, and TableDef.Validate accepts it, but this package does not yet know
// how to build DDL for a NOT ENFORCED foreign key.
type UnsupportedForeignKeyNotEnforcedError struct {
	// ForeignKey is the name of the foreign key declared NOT ENFORCED.
	ForeignKey string
}

func (e *UnsupportedForeignKeyNotEnforcedError) Error() string {
	return fmt.Sprintf("foreign key %q is NOT ENFORCED, which rasql can describe but not yet render", e.ForeignKey)
}

// Unwrap exposes ErrUnsupportedForeignKeyNotEnforced so
// errors.Is(err, ErrUnsupportedForeignKeyNotEnforced) works alongside
// errors.As against *UnsupportedForeignKeyNotEnforcedError.
func (e *UnsupportedForeignKeyNotEnforcedError) Unwrap() error {
	return ErrUnsupportedForeignKeyNotEnforced
}

// ErrUnsupportedForeignKeyTemporal is the sentinel wrapped by every
// [UnsupportedForeignKeyTemporalError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedForeignKeyTemporal = errors.New("render: unsupported temporal foreign key")

// UnsupportedForeignKeyTemporalError reports that a ForeignKeyDef sets
// [schema.ForeignKeyDef.Temporal]. inspect can describe such a foreign key,
// and TableDef.Validate accepts it, but this package does not yet know how
// to build DDL for a PERIOD foreign key.
type UnsupportedForeignKeyTemporalError struct {
	// ForeignKey is the name of the foreign key marked Temporal.
	ForeignKey string
}

func (e *UnsupportedForeignKeyTemporalError) Error() string {
	return fmt.Sprintf("foreign key %q is temporal, which rasql can describe but not yet render", e.ForeignKey)
}

// Unwrap exposes ErrUnsupportedForeignKeyTemporal so
// errors.Is(err, ErrUnsupportedForeignKeyTemporal) works alongside
// errors.As against *UnsupportedForeignKeyTemporalError.
func (e *UnsupportedForeignKeyTemporalError) Unwrap() error {
	return ErrUnsupportedForeignKeyTemporal
}

// ErrUnsupportedForeignKeyDeleteSetColumns is the sentinel wrapped by every
// [UnsupportedForeignKeyDeleteSetColumnsError], so a caller that only needs
// a presence check can use errors.Is instead of errors.As.
var ErrUnsupportedForeignKeyDeleteSetColumns = errors.New("render: unsupported foreign key delete set columns")

// UnsupportedForeignKeyDeleteSetColumnsError reports that a ForeignKeyDef
// names [schema.ForeignKeyDef.DeleteSetColumns]. inspect can describe such
// a foreign key, and TableDef.Validate accepts it, but this package does
// not yet know how to build DDL for an ON DELETE SET NULL/SET DEFAULT
// column list.
type UnsupportedForeignKeyDeleteSetColumnsError struct {
	// ForeignKey is the name of the foreign key that named delete-set
	// columns.
	ForeignKey string
	// DeleteSetColumns is the column list the foreign key named.
	DeleteSetColumns []string
}

func (e *UnsupportedForeignKeyDeleteSetColumnsError) Error() string {
	return fmt.Sprintf("foreign key %q names ON DELETE SET columns %v, which rasql can describe but not yet render", e.ForeignKey, e.DeleteSetColumns)
}

// Unwrap exposes ErrUnsupportedForeignKeyDeleteSetColumns so
// errors.Is(err, ErrUnsupportedForeignKeyDeleteSetColumns) works alongside
// errors.As against *UnsupportedForeignKeyDeleteSetColumnsError.
func (e *UnsupportedForeignKeyDeleteSetColumnsError) Unwrap() error {
	return ErrUnsupportedForeignKeyDeleteSetColumns
}

// ErrUnsupportedUniqueDeferrability is the sentinel wrapped by every
// [UnsupportedUniqueDeferrabilityError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueDeferrability = errors.New("render: unsupported unique constraint deferrability")

// UnsupportedUniqueDeferrabilityError reports that a UniqueDef names a
// non-default [schema.Deferrability]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for anything other than a plain NOT DEFERRABLE
// unique constraint.
type UnsupportedUniqueDeferrabilityError struct {
	// Unique is the name of the unique constraint that named a
	// non-default deferrability.
	Unique string
	// Deferrable is the non-default deferrability the constraint named.
	Deferrable schema.Deferrability
}

func (e *UnsupportedUniqueDeferrabilityError) Error() string {
	return fmt.Sprintf("unique constraint %q is %s, which rasql can describe but not yet render", e.Unique, e.Deferrable)
}

// Unwrap exposes ErrUnsupportedUniqueDeferrability so
// errors.Is(err, ErrUnsupportedUniqueDeferrability) works alongside
// errors.As against *UnsupportedUniqueDeferrabilityError.
func (e *UnsupportedUniqueDeferrabilityError) Unwrap() error {
	return ErrUnsupportedUniqueDeferrability
}

// ErrUnsupportedUniqueNullsNotDistinct is the sentinel wrapped by every
// [UnsupportedUniqueNullsNotDistinctError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueNullsNotDistinct = errors.New("render: unsupported unique constraint nulls-not-distinct")

// UnsupportedUniqueNullsNotDistinctError reports that a UniqueDef sets
// [schema.UniqueDef.NullsNotDistinct]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a NULLS NOT DISTINCT clause.
type UnsupportedUniqueNullsNotDistinctError struct {
	// Unique is the name of the unique constraint that set
	// NullsNotDistinct.
	Unique string
}

func (e *UnsupportedUniqueNullsNotDistinctError) Error() string {
	return fmt.Sprintf("unique constraint %q uses NULLS NOT DISTINCT, which rasql can describe but not yet render", e.Unique)
}

// Unwrap exposes ErrUnsupportedUniqueNullsNotDistinct so
// errors.Is(err, ErrUnsupportedUniqueNullsNotDistinct) works alongside
// errors.As against *UnsupportedUniqueNullsNotDistinctError.
func (e *UnsupportedUniqueNullsNotDistinctError) Unwrap() error {
	return ErrUnsupportedUniqueNullsNotDistinct
}

// ErrUnsupportedUniqueIncludeColumns is the sentinel wrapped by every
// [UnsupportedUniqueIncludeColumnsError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueIncludeColumns = errors.New("render: unsupported unique constraint include columns")

// UnsupportedUniqueIncludeColumnsError reports that a UniqueDef names
// [schema.UniqueDef.IncludeColumns]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for an INCLUDE clause.
type UnsupportedUniqueIncludeColumnsError struct {
	// Unique is the name of the unique constraint that named included
	// columns.
	Unique string
	// IncludeColumns is the ordered list of included column names the
	// constraint named.
	IncludeColumns []string
}

func (e *UnsupportedUniqueIncludeColumnsError) Error() string {
	return fmt.Sprintf("unique constraint %q includes columns %q, which rasql can describe but not yet render", e.Unique, e.IncludeColumns)
}

// Unwrap exposes ErrUnsupportedUniqueIncludeColumns so
// errors.Is(err, ErrUnsupportedUniqueIncludeColumns) works alongside
// errors.As against *UnsupportedUniqueIncludeColumnsError.
func (e *UnsupportedUniqueIncludeColumnsError) Unwrap() error {
	return ErrUnsupportedUniqueIncludeColumns
}

// ErrUnsupportedUniqueConflictResolution is the sentinel wrapped by every
// [UnsupportedUniqueConflictResolutionError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueConflictResolution = errors.New("render: unsupported unique constraint conflict resolution")

// UnsupportedUniqueConflictResolutionError reports that a UniqueDef names a
// non-default [schema.ConflictResolution]. inspect can describe such a
// unique constraint, and TableDef.Validate accepts it, but this package
// does not yet know how to build DDL for an ON CONFLICT clause.
type UnsupportedUniqueConflictResolutionError struct {
	// Unique is the name of the unique constraint that named a
	// non-default conflict resolution.
	Unique string
	// OnConflict is the non-default conflict resolution the constraint
	// named.
	OnConflict schema.ConflictResolution
}

func (e *UnsupportedUniqueConflictResolutionError) Error() string {
	return fmt.Sprintf("unique constraint %q uses ON CONFLICT %s, which rasql can describe but not yet render", e.Unique, e.OnConflict)
}

// Unwrap exposes ErrUnsupportedUniqueConflictResolution so
// errors.Is(err, ErrUnsupportedUniqueConflictResolution) works alongside
// errors.As against *UnsupportedUniqueConflictResolutionError.
func (e *UnsupportedUniqueConflictResolutionError) Unwrap() error {
	return ErrUnsupportedUniqueConflictResolution
}

// ErrUnsupportedUniqueKeyDetails is the sentinel wrapped by every
// [UnsupportedUniqueKeyDetailsError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueKeyDetails = errors.New("render: unsupported unique constraint key details")

// UnsupportedUniqueKeyDetailsError reports that a UniqueDef names
// [schema.UniqueDef.Keys], meaning at least one of its keys is ordered DESC
// or names a non-default collation. inspect can describe such a
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for one.
type UnsupportedUniqueKeyDetailsError struct {
	// Unique is the name of the constraint, or empty for an unnamed one.
	Unique string
	// Keys is the constraint's own per-key facts.
	Keys []schema.IndexKeyDef
}

func (e *UnsupportedUniqueKeyDetailsError) Error() string {
	return fmt.Sprintf("unique constraint %q has per-key details %+v, which rasql can describe but not yet render", e.Unique, e.Keys)
}

// Unwrap exposes ErrUnsupportedUniqueKeyDetails so errors.Is(err,
// ErrUnsupportedUniqueKeyDetails) works alongside errors.As against
// *UnsupportedUniqueKeyDetailsError.
func (e *UnsupportedUniqueKeyDetailsError) Unwrap() error {
	return ErrUnsupportedUniqueKeyDetails
}

// ErrUnsupportedUniqueTemporal is the sentinel wrapped by every
// [UnsupportedUniqueTemporalError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueTemporal = errors.New("render: unsupported temporal unique constraint")

// UnsupportedUniqueTemporalError reports that a UniqueDef sets
// [schema.UniqueDef.Temporal]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a WITHOUT OVERLAPS clause.
type UnsupportedUniqueTemporalError struct {
	// Unique is the name of the unique constraint marked Temporal.
	Unique string
}

func (e *UnsupportedUniqueTemporalError) Error() string {
	return fmt.Sprintf("unique constraint %q is temporal, which rasql can describe but not yet render", e.Unique)
}

// Unwrap exposes ErrUnsupportedUniqueTemporal so
// errors.Is(err, ErrUnsupportedUniqueTemporal) works alongside errors.As
// against *UnsupportedUniqueTemporalError.
func (e *UnsupportedUniqueTemporalError) Unwrap() error {
	return ErrUnsupportedUniqueTemporal
}

// ErrUnsupportedUniqueStorageParameters is the sentinel wrapped by every
// [UnsupportedUniqueStorageParametersError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueStorageParameters = errors.New("render: unsupported unique constraint storage parameters")

// UnsupportedUniqueStorageParametersError reports that a UniqueDef names
// [schema.UniqueDef.StorageParameters]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a WITH (...) storage-parameters clause.
type UnsupportedUniqueStorageParametersError struct {
	// Unique is the name of the unique constraint that named storage
	// parameters.
	Unique string
	// StorageParameters is the storage parameters the constraint named.
	StorageParameters map[string]string
}

func (e *UnsupportedUniqueStorageParametersError) Error() string {
	return fmt.Sprintf("unique constraint %q has storage parameters %v, which rasql can describe but not yet render", e.Unique, e.StorageParameters)
}

// Unwrap exposes ErrUnsupportedUniqueStorageParameters so
// errors.Is(err, ErrUnsupportedUniqueStorageParameters) works alongside
// errors.As against *UnsupportedUniqueStorageParametersError.
func (e *UnsupportedUniqueStorageParametersError) Unwrap() error {
	return ErrUnsupportedUniqueStorageParameters
}

// ErrUnsupportedUniqueTablespace is the sentinel wrapped by every
// [UnsupportedUniqueTablespaceError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueTablespace = errors.New("render: unsupported unique constraint tablespace")

// UnsupportedUniqueTablespaceError reports that a UniqueDef names a
// [schema.UniqueDef.Tablespace]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a TABLESPACE clause.
type UnsupportedUniqueTablespaceError struct {
	// Unique is the name of the unique constraint that named a
	// tablespace.
	Unique string
	// Tablespace is the tablespace the constraint named.
	Tablespace string
}

func (e *UnsupportedUniqueTablespaceError) Error() string {
	return fmt.Sprintf("unique constraint %q uses tablespace %q, which rasql can describe but not yet render", e.Unique, e.Tablespace)
}

// Unwrap exposes ErrUnsupportedUniqueTablespace so
// errors.Is(err, ErrUnsupportedUniqueTablespace) works alongside errors.As
// against *UnsupportedUniqueTablespaceError.
func (e *UnsupportedUniqueTablespaceError) Unwrap() error {
	return ErrUnsupportedUniqueTablespace
}

// ErrUnsupportedUniqueReplicaIdentity is the sentinel wrapped by every
// [UnsupportedUniqueReplicaIdentityError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueReplicaIdentity = errors.New("render: unsupported unique constraint replica identity")

// UnsupportedUniqueReplicaIdentityError reports that a UniqueDef sets
// [schema.UniqueDef.ReplicaIdentity]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a REPLICA IDENTITY USING INDEX declaration.
type UnsupportedUniqueReplicaIdentityError struct {
	// Unique is the name of the unique constraint marked as the replica
	// identity.
	Unique string
}

func (e *UnsupportedUniqueReplicaIdentityError) Error() string {
	return fmt.Sprintf("unique constraint %q is the replica identity, which rasql can describe but not yet render", e.Unique)
}

// Unwrap exposes ErrUnsupportedUniqueReplicaIdentity so
// errors.Is(err, ErrUnsupportedUniqueReplicaIdentity) works alongside
// errors.As against *UnsupportedUniqueReplicaIdentityError.
func (e *UnsupportedUniqueReplicaIdentityError) Unwrap() error {
	return ErrUnsupportedUniqueReplicaIdentity
}

// ErrUnsupportedUniqueCollations is the sentinel wrapped by every
// [UnsupportedUniqueCollationsError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedUniqueCollations = errors.New("render: unsupported unique constraint column collations")

// UnsupportedUniqueCollationsError reports that a UniqueDef names
// [schema.UniqueDef.Collations]. inspect can describe such a unique
// constraint, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for a non-default COLLATE clause on a unique
// constraint's column.
type UnsupportedUniqueCollationsError struct {
	// Unique is the name of the unique constraint that named a
	// non-default collation.
	Unique string
	// Collations is the per-column collations the constraint named.
	Collations map[string]string
}

func (e *UnsupportedUniqueCollationsError) Error() string {
	return fmt.Sprintf("unique constraint %q has column collations %v, which rasql can describe but not yet render", e.Unique, e.Collations)
}

// Unwrap exposes ErrUnsupportedUniqueCollations so
// errors.Is(err, ErrUnsupportedUniqueCollations) works alongside errors.As
// against *UnsupportedUniqueCollationsError.
func (e *UnsupportedUniqueCollationsError) Unwrap() error {
	return ErrUnsupportedUniqueCollations
}

// ErrUnsupportedTableStrict is the sentinel wrapped by every
// [UnsupportedTableStrictError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedTableStrict = errors.New("render: unsupported STRICT table")

// UnsupportedTableStrictError reports that a TableDef sets
// [schema.TableDef.Strict]. inspect can describe such a table, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for a STRICT table.
type UnsupportedTableStrictError struct {
	// Table is the name of the table declared STRICT.
	Table string
}

func (e *UnsupportedTableStrictError) Error() string {
	return fmt.Sprintf("table %q is STRICT, which rasql can describe but not yet render", e.Table)
}

// Unwrap exposes ErrUnsupportedTableStrict so errors.Is(err,
// ErrUnsupportedTableStrict) works alongside errors.As against
// *UnsupportedTableStrictError.
func (e *UnsupportedTableStrictError) Unwrap() error {
	return ErrUnsupportedTableStrict
}

// ErrUnsupportedTableWithoutRowID is the sentinel wrapped by every
// [UnsupportedTableWithoutRowIDError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedTableWithoutRowID = errors.New("render: unsupported WITHOUT ROWID table")

// UnsupportedTableWithoutRowIDError reports that a TableDef sets
// [schema.TableDef.WithoutRowID]. inspect can describe such a table, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for a WITHOUT ROWID table.
type UnsupportedTableWithoutRowIDError struct {
	// Table is the name of the table declared WITHOUT ROWID.
	Table string
}

func (e *UnsupportedTableWithoutRowIDError) Error() string {
	return fmt.Sprintf("table %q is WITHOUT ROWID, which rasql can describe but not yet render", e.Table)
}

// Unwrap exposes ErrUnsupportedTableWithoutRowID so errors.Is(err,
// ErrUnsupportedTableWithoutRowID) works alongside errors.As against
// *UnsupportedTableWithoutRowIDError.
func (e *UnsupportedTableWithoutRowIDError) Unwrap() error {
	return ErrUnsupportedTableWithoutRowID
}

// ErrUnsupportedVirtualTable is the sentinel wrapped by every
// [UnsupportedVirtualTableError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedVirtualTable = errors.New("render: unsupported SQLite virtual table")

// UnsupportedVirtualTableError reports that a TableDef names
// [schema.TableDef.VirtualTableModule]. inspect can describe such a table,
// and TableDef.Validate accepts it, but this package does not yet know how
// to build a CREATE VIRTUAL TABLE statement for one.
type UnsupportedVirtualTableError struct {
	// Table is the name of the virtual table.
	Table string
	// Module is the virtual table's own module name.
	Module string
}

func (e *UnsupportedVirtualTableError) Error() string {
	return fmt.Sprintf("table %q is a virtual table using module %q, which rasql can describe but not yet render", e.Table, e.Module)
}

// Unwrap exposes ErrUnsupportedVirtualTable so errors.Is(err,
// ErrUnsupportedVirtualTable) works alongside errors.As against
// *UnsupportedVirtualTableError.
func (e *UnsupportedVirtualTableError) Unwrap() error {
	return ErrUnsupportedVirtualTable
}

// ErrUnsupportedPrimaryKeyAutoincrement is the sentinel wrapped by every
// [UnsupportedPrimaryKeyAutoincrementError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedPrimaryKeyAutoincrement = errors.New("render: unsupported primary key AUTOINCREMENT")

// UnsupportedPrimaryKeyAutoincrementError reports that a TableDef sets
// [schema.TableDef.PrimaryKeyAutoincrement]. inspect can describe such a
// primary key, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for an AUTOINCREMENT primary key.
type UnsupportedPrimaryKeyAutoincrementError struct {
	// Table is the name of the table whose primary key named
	// AUTOINCREMENT.
	Table string
}

func (e *UnsupportedPrimaryKeyAutoincrementError) Error() string {
	return fmt.Sprintf("table %q's primary key is AUTOINCREMENT, which rasql can describe but not yet render", e.Table)
}

// Unwrap exposes ErrUnsupportedPrimaryKeyAutoincrement so errors.Is(err,
// ErrUnsupportedPrimaryKeyAutoincrement) works alongside errors.As against
// *UnsupportedPrimaryKeyAutoincrementError.
func (e *UnsupportedPrimaryKeyAutoincrementError) Unwrap() error {
	return ErrUnsupportedPrimaryKeyAutoincrement
}

// ErrUnsupportedPrimaryKeyConflictResolution is the sentinel wrapped by
// every [UnsupportedPrimaryKeyConflictResolutionError], so a caller that
// only needs a presence check can use errors.Is instead of errors.As.
var ErrUnsupportedPrimaryKeyConflictResolution = errors.New("render: unsupported primary key conflict resolution")

// UnsupportedPrimaryKeyConflictResolutionError reports that a TableDef
// names a non-default [schema.ConflictResolution] on its primary key via
// [schema.TableDef.PrimaryKeyOnConflict]. inspect can describe such a
// primary key, and TableDef.Validate accepts it, but this package does not
// yet know how to build DDL for an ON CONFLICT clause on a primary key.
type UnsupportedPrimaryKeyConflictResolutionError struct {
	// Table is the name of the table whose primary key named a non-default
	// conflict resolution.
	Table string
	// OnConflict is the non-default conflict resolution the primary key
	// named.
	OnConflict schema.ConflictResolution
}

func (e *UnsupportedPrimaryKeyConflictResolutionError) Error() string {
	return fmt.Sprintf("table %q's primary key uses ON CONFLICT %s, which rasql can describe but not yet render", e.Table, e.OnConflict)
}

// Unwrap exposes ErrUnsupportedPrimaryKeyConflictResolution so
// errors.Is(err, ErrUnsupportedPrimaryKeyConflictResolution) works
// alongside errors.As against *UnsupportedPrimaryKeyConflictResolutionError.
func (e *UnsupportedPrimaryKeyConflictResolutionError) Unwrap() error {
	return ErrUnsupportedPrimaryKeyConflictResolution
}

// ErrUnsupportedGeneratedColumn is the sentinel wrapped by every
// [UnsupportedGeneratedColumnError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedGeneratedColumn = errors.New("render: unsupported generated column")

// UnsupportedGeneratedColumnError reports that a ColumnDef names a
// [schema.ColumnDef.GeneratedExpression]. inspect can describe such a
// column, and TableDef.Validate accepts it, but this package does not yet
// know how to build a GENERATED ALWAYS AS clause, and a generated column
// cannot be written to like an ordinary one.
type UnsupportedGeneratedColumnError struct {
	// Column is the name of the column that named a generated expression.
	Column string
	// Expression is the generated expression the column named.
	Expression string
	// Storage is the column's schema.GeneratedStorage.
	Storage schema.GeneratedStorage
}

func (e *UnsupportedGeneratedColumnError) Error() string {
	return fmt.Sprintf("column %q is generated as %s (%s), which rasql can describe but not yet render", e.Column, e.Expression, e.Storage)
}

// Unwrap exposes ErrUnsupportedGeneratedColumn so
// errors.Is(err, ErrUnsupportedGeneratedColumn) works alongside errors.As
// against *UnsupportedGeneratedColumnError.
func (e *UnsupportedGeneratedColumnError) Unwrap() error {
	return ErrUnsupportedGeneratedColumn
}

// ErrUnsupportedIdentityColumn is the sentinel wrapped by every
// [UnsupportedIdentityColumnError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIdentityColumn = errors.New("render: unsupported identity column")

// UnsupportedIdentityColumnError reports that a ColumnDef names a
// [schema.ColumnDef.Identity] this package cannot render for the given
// dialect. inspect can describe such a column, and TableDef.Validate
// accepts it, but Reason distinguishes three different refusals that would
// otherwise need three near-identical error types: a dialect with no
// identity feature at all (every dialect but PostgreSQL and MySQL), a
// dialect whose identity feature has no form that rejects an explicit
// value (MySQL's AUTO_INCREMENT, which cannot render
// [schema.IdentityAlways]), and MySQL's own requirement that an identity
// column be the leading column of some key, and that at most one column
// of a table be an identity column.
type UnsupportedIdentityColumnError struct {
	// Column is the name of the column that named an identity generation.
	Column string
	// Identity is the identity generation the column named.
	Identity schema.IdentityGeneration
	// Dialect is the name of the dialect that cannot render this column.
	Dialect string
	// Reason states, in one clause, why this dialect cannot render this
	// column's identity generation.
	Reason string
}

func (e *UnsupportedIdentityColumnError) Error() string {
	return fmt.Sprintf("column %q is %s IDENTITY, which the %s dialect cannot render: %s", e.Column, e.Identity, e.Dialect, e.Reason)
}

// Unwrap exposes ErrUnsupportedIdentityColumn so
// errors.Is(err, ErrUnsupportedIdentityColumn) works alongside errors.As
// against *UnsupportedIdentityColumnError.
func (e *UnsupportedIdentityColumnError) Unwrap() error {
	return ErrUnsupportedIdentityColumn
}

// ErrUnsupportedIntegerDisplayWidth is the sentinel wrapped by every
// [UnsupportedIntegerDisplayWidthError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedIntegerDisplayWidth = errors.New("render: unsupported integer display width")

// UnsupportedIntegerDisplayWidthError reports that a ColumnDef names a
// stated [schema.IntegerType.DisplayWidth], such as the 11 in int(11).
// inspect can describe such a column, and TableDef.Validate accepts it, but
// this package does not yet know how to build an INT(n) declaration.
type UnsupportedIntegerDisplayWidthError struct {
	// Column is the name of the column that named a display width.
	Column string
	// Width is the display width the column named.
	Width int
}

func (e *UnsupportedIntegerDisplayWidthError) Error() string {
	return fmt.Sprintf("column %q states an integer display width of %d, which rasql can describe but not yet render", e.Column, e.Width)
}

// Unwrap exposes ErrUnsupportedIntegerDisplayWidth so
// errors.Is(err, ErrUnsupportedIntegerDisplayWidth) works alongside
// errors.As against *UnsupportedIntegerDisplayWidthError.
func (e *UnsupportedIntegerDisplayWidthError) Unwrap() error {
	return ErrUnsupportedIntegerDisplayWidth
}

// ErrUnsupportedIntegerZeroFill is the sentinel wrapped by every
// [UnsupportedIntegerZeroFillError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIntegerZeroFill = errors.New("render: unsupported integer zerofill")

// UnsupportedIntegerZeroFillError reports that a ColumnDef names a true
// [schema.IntegerType.ZeroFill]. inspect can describe such a column, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build a ZEROFILL declaration.
type UnsupportedIntegerZeroFillError struct {
	// Column is the name of the column that carried ZEROFILL.
	Column string
}

func (e *UnsupportedIntegerZeroFillError) Error() string {
	return fmt.Sprintf("column %q carries ZEROFILL, which rasql can describe but not yet render", e.Column)
}

// Unwrap exposes ErrUnsupportedIntegerZeroFill so
// errors.Is(err, ErrUnsupportedIntegerZeroFill) works alongside errors.As
// against *UnsupportedIntegerZeroFillError.
func (e *UnsupportedIntegerZeroFillError) Unwrap() error {
	return ErrUnsupportedIntegerZeroFill
}

// ErrUnsupportedDecimalUnsigned is the sentinel wrapped by every
// [UnsupportedDecimalUnsignedError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedDecimalUnsigned = errors.New("render: unsupported decimal unsigned")

// UnsupportedDecimalUnsignedError reports that a ColumnDef names a true
// [schema.DecimalType.Unsigned]. inspect can describe such a column, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build an UNSIGNED decimal declaration.
type UnsupportedDecimalUnsignedError struct {
	// Column is the name of the column that carried UNSIGNED.
	Column string
}

func (e *UnsupportedDecimalUnsignedError) Error() string {
	return fmt.Sprintf("column %q carries UNSIGNED, which rasql can describe but not yet render", e.Column)
}

// Unwrap exposes ErrUnsupportedDecimalUnsigned so
// errors.Is(err, ErrUnsupportedDecimalUnsigned) works alongside errors.As
// against *UnsupportedDecimalUnsignedError.
func (e *UnsupportedDecimalUnsignedError) Unwrap() error {
	return ErrUnsupportedDecimalUnsigned
}

// ErrUnsupportedDecimalZeroFill is the sentinel wrapped by every
// [UnsupportedDecimalZeroFillError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedDecimalZeroFill = errors.New("render: unsupported decimal zerofill")

// UnsupportedDecimalZeroFillError reports that a ColumnDef names a true
// [schema.DecimalType.ZeroFill]. inspect can describe such a column, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build a ZEROFILL decimal declaration.
type UnsupportedDecimalZeroFillError struct {
	// Column is the name of the column that carried ZEROFILL.
	Column string
}

func (e *UnsupportedDecimalZeroFillError) Error() string {
	return fmt.Sprintf("column %q carries ZEROFILL, which rasql can describe but not yet render", e.Column)
}

// Unwrap exposes ErrUnsupportedDecimalZeroFill so
// errors.Is(err, ErrUnsupportedDecimalZeroFill) works alongside errors.As
// against *UnsupportedDecimalZeroFillError.
func (e *UnsupportedDecimalZeroFillError) Unwrap() error {
	return ErrUnsupportedDecimalZeroFill
}

// ErrUnsupportedExclusionConstraint is the sentinel wrapped by every
// [UnsupportedExclusionConstraintError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedExclusionConstraint = errors.New("render: unsupported exclusion constraint")

// UnsupportedExclusionConstraintError reports that a TableDef names an
// [schema.ExclusionDef]. inspect can describe such a constraint, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for an EXCLUDE clause.
type UnsupportedExclusionConstraintError struct {
	// Exclusion is the name of the exclusion constraint.
	Exclusion string
}

func (e *UnsupportedExclusionConstraintError) Error() string {
	return fmt.Sprintf("exclusion constraint %q is an EXCLUDE constraint, which rasql can describe but not yet render", e.Exclusion)
}

// Unwrap exposes ErrUnsupportedExclusionConstraint so
// errors.Is(err, ErrUnsupportedExclusionConstraint) works alongside
// errors.As against *UnsupportedExclusionConstraintError.
func (e *UnsupportedExclusionConstraintError) Unwrap() error {
	return ErrUnsupportedExclusionConstraint
}

// CreateTable renders a CREATE TABLE statement for table.
func CreateTable(d dialect.Dialect, table schema.TableDef) (statement.Statement, error) {
	if isNilDialect(d) {
		return statement.Statement{}, &Error{Err: fmt.Errorf("dialect must not be nil")}
	}
	if err := table.Validate(); err != nil {
		return statement.Statement{}, &Error{Dialect: d.Name(), Err: fmt.Errorf("invalid table: %w", err)}
	}
	renderer := renderer{dialect: d}
	if err := renderer.writeCreateTable(table); err != nil {
		return statement.Statement{}, &Error{Dialect: d.Name(), Err: err}
	}
	return statement.New(sqltext.Text(renderer.builder.String())), nil
}

// CreateIndexes renders the CREATE INDEX statements for table.
func CreateIndexes(d dialect.Dialect, table schema.TableDef) ([]statement.Statement, error) {
	if isNilDialect(d) {
		return nil, &Error{Err: fmt.Errorf("dialect must not be nil")}
	}
	if err := table.Validate(); err != nil {
		return nil, &Error{Dialect: d.Name(), Err: fmt.Errorf("invalid table: %w", err)}
	}
	statements := make([]statement.Statement, len(table.Indexes))
	for i, index := range table.Indexes {
		renderer := renderer{dialect: d}
		if err := renderer.writeCreateIndex(table, index); err != nil {
			return nil, &Error{Dialect: d.Name(), Err: err}
		}
		statements[i] = statement.New(sqltext.Text(renderer.builder.String()))
	}
	return statements, nil
}

func (r *renderer) writeCreateTable(table schema.TableDef) error {
	if table.VirtualTableModule != "" {
		return &UnsupportedVirtualTableError{Table: table.Name, Module: table.VirtualTableModule}
	}
	if table.Strict {
		return &UnsupportedTableStrictError{Table: table.Name}
	}
	if table.WithoutRowID {
		return &UnsupportedTableWithoutRowIDError{Table: table.Name}
	}
	if r.dialect.Name() == "mysql" {
		if err := r.checkMySQLIdentityKeyness(table); err != nil {
			return err
		}
	}
	name, err := r.quoteQualified(table.Schema, table.Name)
	if err != nil {
		return err
	}
	r.builder.WriteString("CREATE TABLE ")
	r.builder.WriteString(name)
	r.builder.WriteString(" (")

	definitions := make([]string, 0, len(table.Columns)+len(table.UniqueConstraints)+len(table.Checks)+len(table.ForeignKeys)+1)
	for _, column := range table.Columns {
		definition, err := r.columnDefinition(column)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	if len(table.PrimaryKey) > 0 {
		if table.PrimaryKeyAutoincrement {
			return &UnsupportedPrimaryKeyAutoincrementError{Table: table.Name}
		}
		if table.PrimaryKeyOnConflict != "" {
			return &UnsupportedPrimaryKeyConflictResolutionError{Table: table.Name, OnConflict: table.PrimaryKeyOnConflict}
		}
		if err := r.rejectUnboundedMySQLText(table, table.PrimaryKey, "a primary key"); err != nil {
			return err
		}
		columns, err := r.quotedNames(table.PrimaryKey)
		if err != nil {
			return err
		}
		definitions = append(definitions, "PRIMARY KEY ("+strings.Join(columns, ", ")+")")
	}
	for _, constraint := range table.UniqueConstraints {
		if constraint.Deferrable != "" {
			return &UnsupportedUniqueDeferrabilityError{Unique: constraint.Name, Deferrable: constraint.Deferrable}
		}
		if constraint.NullsNotDistinct {
			return &UnsupportedUniqueNullsNotDistinctError{Unique: constraint.Name}
		}
		if len(constraint.IncludeColumns) > 0 {
			return &UnsupportedUniqueIncludeColumnsError{Unique: constraint.Name, IncludeColumns: constraint.IncludeColumns}
		}
		if constraint.OnConflict != "" {
			return &UnsupportedUniqueConflictResolutionError{Unique: constraint.Name, OnConflict: constraint.OnConflict}
		}
		if len(constraint.Keys) > 0 {
			return &UnsupportedUniqueKeyDetailsError{Unique: constraint.Name, Keys: constraint.Keys}
		}
		if constraint.Temporal {
			return &UnsupportedUniqueTemporalError{Unique: constraint.Name}
		}
		if len(constraint.StorageParameters) > 0 {
			return &UnsupportedUniqueStorageParametersError{Unique: constraint.Name, StorageParameters: constraint.StorageParameters}
		}
		if constraint.Tablespace != "" {
			return &UnsupportedUniqueTablespaceError{Unique: constraint.Name, Tablespace: constraint.Tablespace}
		}
		if constraint.ReplicaIdentity {
			return &UnsupportedUniqueReplicaIdentityError{Unique: constraint.Name}
		}
		if len(constraint.Collations) > 0 {
			return &UnsupportedUniqueCollationsError{Unique: constraint.Name, Collations: constraint.Collations}
		}
		if err := r.rejectUnboundedMySQLText(table, constraint.Columns, "a unique constraint"); err != nil {
			return err
		}
		columns, err := r.quotedNames(constraint.Columns)
		if err != nil {
			return err
		}
		definition := "UNIQUE (" + strings.Join(columns, ", ") + ")"
		if constraint.Name != "" {
			name, err := r.quoteIdentifier(constraint.Name)
			if err != nil {
				return err
			}
			definition = "CONSTRAINT " + name + " " + definition
		}
		definitions = append(definitions, definition)
	}
	for _, check := range table.Checks {
		if check.NoInherit {
			return &UnsupportedCheckNoInheritError{Check: check.Name}
		}
		if check.NotValid {
			return &UnsupportedCheckNotValidError{Check: check.Name}
		}
		if check.NotEnforced {
			return &UnsupportedCheckNotEnforcedError{Check: check.Name}
		}
		definition := "CHECK (" + string(check.Expression) + ")"
		if check.Name != "" {
			name, err := r.quoteIdentifier(check.Name)
			if err != nil {
				return err
			}
			definition = "CONSTRAINT " + name + " " + definition
		}
		definitions = append(definitions, definition)
	}
	for _, exclusion := range table.ExclusionConstraints {
		return &UnsupportedExclusionConstraintError{Exclusion: exclusion.Name}
	}
	for _, key := range table.ForeignKeys {
		definition, err := r.foreignKeyDefinition(table, key)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	r.builder.WriteString(strings.Join(definitions, ", "))
	r.builder.WriteByte(')')
	return nil
}

// checkMySQLIdentityKeyness enforces, before any CREATE TABLE definition is
// built, the two rules MySQL error 1075 ("there can be only one auto
// column and it must be defined as a key") states about an AUTO_INCREMENT
// column: at most one column of a table may be an identity column, and
// that column must be the leading column of table.PrimaryKey or of some
// table.UniqueConstraints entry. Checking this ahead of time, rather than
// letting MySQL reject the resulting CREATE TABLE, matters because a
// CREATE INDEX statement issued afterwards cannot rescue it: the failure
// happens inside CREATE TABLE itself.
func (r *renderer) checkMySQLIdentityKeyness(table schema.TableDef) error {
	var identityColumns []schema.ColumnDef
	for _, column := range table.Columns {
		if column.Identity != "" {
			identityColumns = append(identityColumns, column)
		}
	}
	if len(identityColumns) == 0 {
		return nil
	}
	if len(identityColumns) > 1 {
		second := identityColumns[1]
		return &UnsupportedIdentityColumnError{Column: second.Name, Identity: second.Identity, Dialect: r.dialect.Name(), Reason: "MySQL allows only one auto-increment column per table"}
	}
	identityColumn := identityColumns[0]
	if mysqlIdentityColumnLeadsAKey(table, identityColumn.Name) {
		return nil
	}
	return &UnsupportedIdentityColumnError{Column: identityColumn.Name, Identity: identityColumn.Identity, Dialect: r.dialect.Name(), Reason: "MySQL requires an auto-increment column to be the leading column of a key"}
}

// mysqlIdentityColumnLeadsAKey reports whether name is the first column of
// table.PrimaryKey or the first column of some table.UniqueConstraints
// entry, the shape MySQL's own "must be defined as a key" rule for an
// AUTO_INCREMENT column requires.
func mysqlIdentityColumnLeadsAKey(table schema.TableDef, name string) bool {
	if len(table.PrimaryKey) > 0 && table.PrimaryKey[0] == name {
		return true
	}
	for _, constraint := range table.UniqueConstraints {
		if len(constraint.Columns) > 0 && constraint.Columns[0] == name {
			return true
		}
	}
	return false
}

func (r *renderer) writeCreateIndex(table schema.TableDef, index schema.IndexDef) error {
	if index.Method != "" {
		return &UnsupportedIndexMethodError{Index: index.Name, Method: index.Method}
	}
	if index.Predicate != "" && !r.dialect.Supports(dialect.CapabilityPartialIndex) {
		return &UnsupportedPartialIndexError{Index: index.Name, Predicate: string(index.Predicate), Dialect: r.dialect.Name()}
	}
	if len(index.Expressions) > 0 {
		expressions := make([]string, len(index.Expressions))
		for i, expression := range index.Expressions {
			expressions[i] = string(expression)
		}
		return &UnsupportedExpressionIndexError{Index: index.Name, Expressions: expressions}
	}
	if len(index.IncludeColumns) > 0 {
		return &UnsupportedIndexIncludeColumnsError{Index: index.Name, IncludeColumns: index.IncludeColumns}
	}
	if index.Invisible {
		return &UnsupportedIndexInvisibleError{Index: index.Name}
	}
	if len(index.Keys) > 0 {
		return &UnsupportedIndexKeyDetailsError{Index: index.Name, Keys: index.Keys}
	}
	if index.NotValid {
		return &UnsupportedIndexNotValidError{Index: index.Name}
	}
	if len(index.StorageParameters) > 0 {
		return &UnsupportedIndexStorageParametersError{Index: index.Name, StorageParameters: index.StorageParameters}
	}
	if index.Tablespace != "" {
		return &UnsupportedIndexTablespaceError{Index: index.Name, Tablespace: index.Tablespace}
	}
	if index.ReplicaIdentity {
		return &UnsupportedIndexReplicaIdentityError{Index: index.Name}
	}
	if index.NullsNotDistinct {
		return &UnsupportedIndexNullsNotDistinctError{Index: index.Name}
	}
	if err := r.rejectUnboundedMySQLText(table, index.Columns, "an index"); err != nil {
		return err
	}
	indexName, tableName, err := r.qualifiedIndexNames(table, index)
	if err != nil {
		return err
	}
	columns, err := r.quotedNames(index.Columns)
	if err != nil {
		return err
	}
	r.builder.WriteString("CREATE ")
	if index.Unique {
		r.builder.WriteString("UNIQUE ")
	}
	r.builder.WriteString("INDEX ")
	r.builder.WriteString(indexName)
	r.builder.WriteString(" ON ")
	r.builder.WriteString(tableName)
	r.builder.WriteString(" (")
	r.builder.WriteString(strings.Join(columns, ", "))
	r.builder.WriteByte(')')
	if index.Predicate != "" {
		r.builder.WriteString(" WHERE ")
		r.builder.WriteString(string(index.Predicate))
	}
	return nil
}

// qualifiedIndexNames returns the quoted index name and quoted table name for
// a CREATE INDEX statement. An unqualified table.Schema always yields today's
// unqualified pair, with no capability consulted. A qualified table.Schema
// needs one of two capabilities, because which identifier carries the
// qualifier is positional: dialect.CapabilityQualifiedIndexTarget qualifies
// the indexed table and leaves the index name bare, and
// dialect.CapabilityQualifiedIndexName qualifies the index name and leaves
// the indexed table bare, which is SQLite's form, since it cannot qualify the
// table in "ON table" at all. A dialect with neither capability is refused
// rather than silently dropping the qualifier.
func (r *renderer) qualifiedIndexNames(table schema.TableDef, index schema.IndexDef) (string, string, error) {
	if table.Schema == "" {
		indexName, err := r.quoteIdentifier(index.Name)
		if err != nil {
			return "", "", err
		}
		tableName, err := r.quoteIdentifier(table.Name)
		if err != nil {
			return "", "", err
		}
		return indexName, tableName, nil
	}
	switch {
	case r.dialect.Supports(dialect.CapabilityQualifiedIndexTarget):
		indexName, err := r.quoteIdentifier(index.Name)
		if err != nil {
			return "", "", err
		}
		tableName, err := r.quoteQualified(table.Schema, table.Name)
		if err != nil {
			return "", "", err
		}
		return indexName, tableName, nil
	case r.dialect.Supports(dialect.CapabilityQualifiedIndexName):
		indexName, err := r.quoteQualified(table.Schema, index.Name)
		if err != nil {
			return "", "", err
		}
		tableName, err := r.quoteIdentifier(table.Name)
		if err != nil {
			return "", "", err
		}
		return indexName, tableName, nil
	default:
		return "", "", fmt.Errorf("dialect %s: cannot create an index on table %q in schema %q: this dialect lacks both dialect.CapabilityQualifiedIndexTarget and dialect.CapabilityQualifiedIndexName", r.dialect.Name(), table.Name, table.Schema)
	}
}

// rejectUnboundedMySQLText returns an error if names includes an unbounded
// schema.TextType column and the renderer's dialect is MySQL. MySQL raises
// error 1170 ("BLOB/TEXT column used in key specification without a key
// length") when it is asked to build a key over a TEXT column with no
// stated key length, and rasql has no way to state one on a PRIMARY KEY or
// UNIQUE clause the way MySQL's own DDL can with col(255): schema.IndexDef,
// schema.TableDef.PrimaryKey and schema.UniqueDef all name columns, not
// key-length-qualified expressions. schema.TextType.Width closes the gap
// instead, by letting the column itself state a bound that MySQL's key
// length requirement is then already satisfied by; a caller who hits this
// error states one with schema.Width and the column renders VARCHAR(width)
// rather than TEXT. PostgreSQL and SQLite index, and build a primary key or
// unique constraint over, an unbounded text column natively, so this check
// runs on MySQL only.
func (r *renderer) rejectUnboundedMySQLText(table schema.TableDef, names []string, context string) error {
	if r.dialect.Name() != "mysql" {
		return nil
	}
	for _, name := range names {
		column, ok := table.Column(name)
		if !ok {
			continue
		}
		text, ok := column.Type.(schema.TextType)
		if !ok {
			continue
		}
		if _, stated := text.Width.Value(); stated {
			continue
		}
		return fmt.Errorf("dialect %s: column %q has no stated width: state one with schema.Width to use it in %s, since MySQL cannot build a key over an unbounded text column", r.dialect.Name(), name, context)
	}
	return nil
}

func (r *renderer) columnDefinition(column schema.ColumnDef) (string, error) {
	if column.GeneratedExpression != "" {
		return "", &UnsupportedGeneratedColumnError{Column: column.Name, Expression: string(column.GeneratedExpression), Storage: column.GeneratedStorage}
	}
	if integer, ok := column.Type.(schema.IntegerType); ok {
		if width, stated := integer.DisplayWidth.Value(); stated {
			return "", &UnsupportedIntegerDisplayWidthError{Column: column.Name, Width: width}
		}
		if integer.ZeroFill {
			return "", &UnsupportedIntegerZeroFillError{Column: column.Name}
		}
	}
	if decimal, ok := column.Type.(schema.DecimalType); ok {
		// ZeroFill always implies Unsigned in MySQL, so a decimal carrying
		// both is checked for ZeroFill first: the more specific attribute
		// names the actual refusal rather than the weaker fact it implies.
		if decimal.ZeroFill {
			return "", &UnsupportedDecimalZeroFillError{Column: column.Name}
		}
		if decimal.Unsigned {
			return "", &UnsupportedDecimalUnsignedError{Column: column.Name}
		}
	}
	name, err := r.quoteIdentifier(column.Name)
	if err != nil {
		return "", err
	}
	typeName, err := r.dialect.TypeName(column)
	if err != nil {
		return "", fmt.Errorf("column %q: %w", column.Name, err)
	}
	definition := name + " " + typeName
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.Default != "" {
		definition += " DEFAULT " + string(column.Default)
	}
	if column.Identity != "" {
		clause, err := r.identityClause(column)
		if err != nil {
			return "", err
		}
		definition += clause
	}
	return definition, nil
}

// identityClause returns the DDL fragment, including its own leading
// space, that renders column.Identity on the current dialect, appended
// after a column's type, NOT NULL, and DEFAULT clauses — the order both
// PostgreSQL and MySQL accept.
//
// PostgreSQL renders either generation as its own GENERATED ... AS
// IDENTITY clause. MySQL renders schema.IdentityByDefault as AUTO_INCREMENT,
// since that is the only MySQL form and it is BY DEFAULT-shaped: it accepts
// an explicit value. MySQL has no form that rejects an explicit value, so
// schema.IdentityAlways is refused rather than rendered as AUTO_INCREMENT,
// which would silently weaken the column. Every other dialect, SQLite
// included, has no identity feature at all and refuses both generations.
func (r *renderer) identityClause(column schema.ColumnDef) (string, error) {
	switch r.dialect.Name() {
	case "postgresql":
		switch column.Identity {
		case schema.IdentityAlways:
			return " GENERATED ALWAYS AS IDENTITY", nil
		case schema.IdentityByDefault:
			return " GENERATED BY DEFAULT AS IDENTITY", nil
		}
	case "mysql":
		if column.Identity == schema.IdentityByDefault {
			return " AUTO_INCREMENT", nil
		}
		return "", &UnsupportedIdentityColumnError{Column: column.Name, Identity: column.Identity, Dialect: r.dialect.Name(), Reason: "MySQL's AUTO_INCREMENT has no form that rejects an explicit value"}
	}
	return "", &UnsupportedIdentityColumnError{Column: column.Name, Identity: column.Identity, Dialect: r.dialect.Name(), Reason: "this dialect has no identity column feature"}
}

// qualifiedReferencedTable returns the quoted REFERENCES target for key,
// owned by table. An empty key.ReferencedSchema renders exactly what an
// unqualified reference always has, with no capability consulted.
// dialect.CapabilityQualifiedReference renders the reference qualified, on
// any dialect that has it. Without that capability, a same-schema reference
// still renders unqualified, because dropping a same-schema qualifier changes
// nothing about what the reference means; this is also the only form SQLite
// can render, since it rejects a schema-qualified REFERENCES clause outright,
// even for its own schema. A cross-schema reference on a dialect with neither
// path is refused rather than silently rendered as same-schema or dropped.
func (r *renderer) qualifiedReferencedTable(table schema.TableDef, key schema.ForeignKeyDef) (string, error) {
	if key.ReferencedSchema == "" {
		return r.quoteIdentifier(key.ReferencedTable)
	}
	if r.dialect.Supports(dialect.CapabilityQualifiedReference) {
		return r.quoteQualified(key.ReferencedSchema, key.ReferencedTable)
	}
	if key.ReferencedSchema == table.Schema {
		return r.quoteIdentifier(key.ReferencedTable)
	}
	return "", fmt.Errorf("dialect %s: foreign key on table %q references table %q in schema %q: this dialect lacks dialect.CapabilityQualifiedReference and can only reference table %q's own schema %q", r.dialect.Name(), table.Name, key.ReferencedTable, key.ReferencedSchema, table.Name, table.Schema)
}

func (r *renderer) foreignKeyDefinition(table schema.TableDef, key schema.ForeignKeyDef) (string, error) {
	if key.Match != "" {
		return "", &UnsupportedForeignKeyMatchError{ForeignKey: key.Name, Match: key.Match}
	}
	if key.Deferrable != "" {
		return "", &UnsupportedForeignKeyDeferrabilityError{ForeignKey: key.Name, Deferrable: key.Deferrable}
	}
	if key.NotValid {
		return "", &UnsupportedForeignKeyNotValidError{ForeignKey: key.Name}
	}
	if key.NotEnforced {
		return "", &UnsupportedForeignKeyNotEnforcedError{ForeignKey: key.Name}
	}
	if key.Temporal {
		return "", &UnsupportedForeignKeyTemporalError{ForeignKey: key.Name}
	}
	if len(key.DeleteSetColumns) > 0 {
		return "", &UnsupportedForeignKeyDeleteSetColumnsError{ForeignKey: key.Name, DeleteSetColumns: key.DeleteSetColumns}
	}
	columns, err := r.quotedNames(key.Columns)
	if err != nil {
		return "", err
	}
	referencedTable, err := r.qualifiedReferencedTable(table, key)
	if err != nil {
		return "", err
	}
	referencedColumns, err := r.quotedNames(key.ReferencedColumns)
	if err != nil {
		return "", err
	}
	definition := "FOREIGN KEY (" + strings.Join(columns, ", ") + ") REFERENCES " + referencedTable + " (" + strings.Join(referencedColumns, ", ") + ")"
	if key.Name != "" {
		name, err := r.quoteIdentifier(key.Name)
		if err != nil {
			return "", err
		}
		definition = "CONSTRAINT " + name + " " + definition
	}
	if key.OnDelete != "" && (key.OnDelete != schema.NoAction || r.dialect.Name() != "sqlite") {
		definition += " ON DELETE " + string(key.OnDelete)
	}
	if key.OnUpdate != "" && (key.OnUpdate != schema.NoAction || r.dialect.Name() != "sqlite") {
		definition += " ON UPDATE " + string(key.OnUpdate)
	}
	return definition, nil
}

func (r *renderer) quotedNames(names []string) ([]string, error) {
	quoted := make([]string, len(names))
	for i, name := range names {
		value, err := r.quoteIdentifier(name)
		if err != nil {
			return nil, err
		}
		quoted[i] = value
	}
	return quoted, nil
}
