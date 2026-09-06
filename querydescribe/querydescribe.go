// Package querydescribe validates and describes the result shape of static SQL.
package querydescribe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/schema"
)

type Cardinality uint8

const (
	Many Cardinality = iota
	ZeroOrOne
	ExactlyOne
)

type Column struct {
	Name     string
	Binding  schema.GoBinding
	Nullable bool
}

type Description struct {
	Columns     []Column
	Cardinality Cardinality
}

type Request struct {
	Name        string
	SQL         string
	Parameters  []string
	Tables      []schema.TableDef
	Expected    *Description
	Cardinality Cardinality
}

type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Describer interface {
	Describe(context.Context, Request) (Description, error)
}

var (
	ErrInvalidRequest = errors.New("querydescribe: invalid request")
	ErrIncomplete     = errors.New("querydescribe: incomplete description")
	ErrExpected       = errors.New("querydescribe: expected description mismatch")
)

func cloneDescription(in Description) Description {
	out := in
	out.Columns = make([]Column, len(in.Columns))
	for i, c := range in.Columns {
		out.Columns[i] = c
		out.Columns[i].Binding.Imports = append([]schema.GoImport(nil), c.Binding.Imports...)
	}
	return out
}

func validateRequest(r Request) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: name is blank", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.SQL) == "" {
		return fmt.Errorf("%w: %s has blank SQL", ErrInvalidRequest, r.Name)
	}
	if r.Cardinality > ExactlyOne {
		return fmt.Errorf("%w: %s has invalid cardinality %d", ErrInvalidRequest, r.Name, r.Cardinality)
	}
	return nil
}

func compare(expected, observed Description, name string) error {
	if expected.Cardinality != observed.Cardinality {
		return fmt.Errorf("%w: %s cardinality: expected %d, observed %d", ErrExpected, name, expected.Cardinality, observed.Cardinality)
	}
	if len(expected.Columns) != len(observed.Columns) {
		return fmt.Errorf("%w: %s column count: expected %d, observed %d", ErrExpected, name, len(expected.Columns), len(observed.Columns))
	}
	for i := range expected.Columns {
		e, o := expected.Columns[i], observed.Columns[i]
		if e.Name != o.Name {
			return fmt.Errorf("%w: %s column %d name: expected %q, observed %q", ErrExpected, name, i, e.Name, o.Name)
		}
		if e.Nullable != o.Nullable {
			return fmt.Errorf("%w: %s column %d nullability: expected %t, observed %t", ErrExpected, name, i, e.Nullable, o.Nullable)
		}
		if e.Binding.Type != o.Binding.Type {
			return fmt.Errorf("%w: %s column %d binding.type: expected %q, observed %q", ErrExpected, name, i, e.Binding.Type, o.Binding.Type)
		}
		if e.Binding.NullableType != o.Binding.NullableType {
			return fmt.Errorf("%w: %s column %d binding.nullable_type: expected %q, observed %q", ErrExpected, name, i, e.Binding.NullableType, o.Binding.NullableType)
		}
		if fmt.Sprint(e.Binding.Imports) != fmt.Sprint(o.Binding.Imports) {
			return fmt.Errorf("%w: %s column %d binding.imports: expected %q, observed %q", ErrExpected, name, i, fmt.Sprint(e.Binding.Imports), fmt.Sprint(o.Binding.Imports))
		}
	}
	return nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 && !unicode.IsLetter(r) && r != '_' {
			return false
		}
		if i > 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}
