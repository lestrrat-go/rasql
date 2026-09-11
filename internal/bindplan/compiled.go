package bindplan

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/stmt"
)

// CodecPattern matches a well-formed codec identifier. It lives here because
// Unwrap checks it, and the root package reads the same variable so that one
// pattern governs both.
var CodecPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// Slot is the bind metadata a compiled statement keeps for each of its
// arguments, in placeholder order.
type Slot struct {
	ID         ID
	Codec      string
	PreEncoded bool
}

// Compiled is a rendered statement together with the bind metadata and the
// copiers that let it be handed out again without sharing a buffer.
type Compiled struct {
	Statement stmt.Statement
	Slots     []Slot
	CopyArgs  []ValueCopy
}

// Copy returns the statement with a fresh copy of every bound value, so a
// caller that writes through what it is given cannot change what a later Copy
// produces.
func (c Compiled) Copy() (stmt.Statement, error) {
	args := c.Statement.Args()
	if len(args) != len(c.Slots) || len(args) != len(c.CopyArgs) {
		return stmt.Statement{}, planerr.New("internal_plan", "binds", "statement arguments and bind slots differ")
	}
	for i, copier := range c.CopyArgs {
		if copier == nil {
			return stmt.Statement{}, planerr.New("internal_plan", fmt.Sprintf("binds[%d]", i), "missing bind copier")
		}
		copy, err := copier()
		if err != nil {
			return stmt.Statement{}, copyError(i, err)
		}
		args[i] = copy
	}
	return stmt.New(c.Statement.Text(), args...), nil
}

func copyError(index int, err error) error {
	var planErr *planerr.Error
	if errors.As(err, &planErr) && planErr.Code == "unsnapshotable_bind" {
		return err
	}
	return planerr.Wrap("unsnapshotable_bind", fmt.Sprintf("args[%d]", index), err.Error(), err)
}

// Unwrap replaces each bind token in statement with the value it carries, and
// returns that statement alongside the metadata the tokens held.
func Unwrap(statement stmt.Statement) (Compiled, error) {
	args := statement.Args()
	slots := make([]Slot, len(args))
	copyArgs := make([]ValueCopy, len(args))
	for i, arg := range args {
		token, ok := arg.(Token)
		if ok {
			if token.Err != nil {
				return Compiled{}, planerr.New("unsnapshotable_bind", fmt.Sprintf("args[%d]", i), token.Err.Error())
			}
			if token.Copy == nil {
				return Compiled{}, planerr.New("unsnapshotable_bind", fmt.Sprintf("args[%d]", i), "missing bind copier")
			}
			if token.ID == 0 || token.Copy == nil || (token.Codec != "" && !CodecPattern.MatchString(token.Codec)) || !token.PreEncoded && token.Value == nil && token.Copy == nil {
				return Compiled{}, planerr.New("internal_plan", fmt.Sprintf("binds[%d]", i), "invalid bind token")
			}
			slots[i] = Slot{ID: token.ID, Codec: token.Codec, PreEncoded: token.PreEncoded}
			copyArgs[i] = token.Copy
			value, err := token.Copy()
			if err != nil {
				return Compiled{}, copyError(i, err)
			}
			args[i] = value
			continue
		}
		if named, ok := arg.(sql.NamedArg); ok {
			if token, ok := named.Value.(Token); ok {
				if token.Err != nil {
					return Compiled{}, planerr.New("unsnapshotable_bind", fmt.Sprintf("args[%d]", i), token.Err.Error())
				}
				if token.Copy == nil {
					return Compiled{}, planerr.New("unsnapshotable_bind", fmt.Sprintf("args[%d]", i), "missing bind copier")
				}
				if token.ID == 0 || token.Copy == nil || (token.Codec != "" && !CodecPattern.MatchString(token.Codec)) {
					return Compiled{}, planerr.New("internal_plan", fmt.Sprintf("binds[%d]", i), "invalid bind token")
				}
				slots[i] = Slot{ID: token.ID, Codec: token.Codec, PreEncoded: token.PreEncoded}
				name, tokenCopy := named.Name, token.Copy
				copyArgs[i] = func() (any, error) {
					value, err := tokenCopy()
					if err != nil {
						return nil, err
					}
					return sql.Named(name, value), nil
				}
				value, err := copyArgs[i]()
				if err != nil {
					return Compiled{}, copyError(i, err)
				}
				args[i] = value
				continue
			}
		}
		value, copier, err := Adopt(arg, false)
		if err != nil {
			return Compiled{}, planerr.New("unsnapshotable_bind", fmt.Sprintf("args[%d]", i), err.Error())
		}
		args[i] = value
		copyArgs[i] = copier
	}
	if len(args) != len(slots) || len(args) != len(copyArgs) {
		return Compiled{}, planerr.New("internal_plan", "binds", "statement arguments and bind slots differ")
	}
	return Compiled{Statement: stmt.New(statement.Text(), args...), Slots: slots, CopyArgs: copyArgs}, nil
}

// MatchBaseOccurrences reports where each of base's binds appears in paged,
// matching by identity and in order. It reports an error when a bind is
// missing, reordered, or carries a different codec.
func MatchBaseOccurrences(base, paged Compiled) ([]int, error) {
	if len(base.Slots) != len(base.Statement.Args()) || len(paged.Slots) != len(paged.Statement.Args()) {
		return nil, planerr.New("internal_plan", "binds", "statement arguments and bind slots differ")
	}
	result := make([]int, 0, len(base.Slots))
	next := 0
	for i, want := range base.Slots {
		if want.ID == 0 {
			return nil, planerr.New("unsupported_keyset_bind", fmt.Sprintf("base[%d]", i), "bind has no identity")
		}
		found := -1
		for j := next; j < len(paged.Slots); j++ {
			got := paged.Slots[j]
			if got.ID == want.ID {
				if got.Codec != want.Codec {
					return nil, planerr.New("unsupported_keyset_bind", fmt.Sprintf("paged[%d]", j), "codec differs")
				}
				found = j
				next = j + 1
				break
			}
		}
		if found < 0 {
			return nil, planerr.New("unsupported_keyset_bind", fmt.Sprintf("base[%d]", i), "bind occurrence is missing or reordered")
		}
		result = append(result, found)
	}
	return result, nil
}
