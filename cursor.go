package rasql

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/cursorcodec"
)

type PageDirection uint8

const (
	PageAscending PageDirection = iota + 1
	PageDescending
)

type Cursor string

var ErrInvalidCursor = errors.New("rasql: invalid cursor")

type CursorValueCodec interface {
	EncodeCursor(any) ([]byte, error)
	DecodeCursor([]byte) (any, error)
}

type pageKey[R any] struct {
	term      OrderTerm
	direction PageDirection
	nullable  bool
	codec     string
	typ       reflect.Type
	extract   func(R) (bool, any, error)
}

type PageKey[R any] interface{ pageKeyMarker() }

func (*pageKey[R]) pageKeyMarker() {}

func AscKey[R, T comparable](value Expr[T], extract func(R) T) PageKey[R] {
	return &pageKey[R]{term: AscExpr(value), direction: PageAscending, codec: value.codec,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		extract: func(row R) (bool, any, error) {
			owned, err := copyPageValue(extract(row))
			return true, owned, err
		}}
}

func DescKey[R, T comparable](value Expr[T], extract func(R) T) PageKey[R] {
	return &pageKey[R]{term: DescExpr(value), direction: PageDescending, codec: value.codec,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		extract: func(row R) (bool, any, error) {
			owned, err := copyPageValue(extract(row))
			return true, owned, err
		}}
}

func AscNullKey[R, T comparable](value NullExpr[T], extract func(R) Nullable[T], nulls NullOrder) PageKey[R] {
	return newNullPageKey(value, extract, PageAscending, nulls)
}

func DescNullKey[R, T comparable](value NullExpr[T], extract func(R) Nullable[T], nulls NullOrder) PageKey[R] {
	return newNullPageKey(value, extract, PageDescending, nulls)
}

func newNullPageKey[R, T comparable](value NullExpr[T], extract func(R) Nullable[T], direction PageDirection, nulls NullOrder) PageKey[R] {
	term := AscNull(value, nulls)
	if direction == PageDescending {
		term = DescNull(value, nulls)
	}
	return &pageKey[R]{term: term, direction: direction, nullable: true, codec: value.codec,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		extract: func(row R) (bool, any, error) {
			value := extract(row)
			if !value.Valid {
				return false, nil, nil
			}
			owned, err := copyPageValue(value.Value)
			return true, owned, err
		}}
}

type PageSpec[R any] struct {
	keys         []*pageKey[R]
	uniqueSuffix []*pageKey[R]
}

func NewPageSpec[R any](order []PageKey[R], uniqueSuffix ...PageKey[R]) (PageSpec[R], error) {
	if len(order) == 0 {
		return PageSpec[R]{}, planError("invalid_page_spec", "order", "must not be empty")
	}
	if len(uniqueSuffix) == 0 {
		return PageSpec[R]{}, planError("order_not_unique", "uniqueSuffix", "must not be empty")
	}
	if len(order) > 255 {
		return PageSpec[R]{}, planError("invalid_page_spec", "order", "must contain at most 255 keys")
	}
	keys := make([]*pageKey[R], len(order))
	for i, item := range order {
		key, ok := item.(*pageKey[R])
		if !ok || key == nil || key.term.node == nil {
			return PageSpec[R]{}, planError("invalid_page_spec", fmt.Sprintf("order[%d]", i), "must be a valid page key")
		}
		if key.nullable && key.term.nulls == NullOrderDefault {
			return PageSpec[R]{}, planError("invalid_page_spec", fmt.Sprintf("order[%d]", i), "nullable keys require explicit NULL order")
		}
		keys[i] = key
	}
	suffix := make([]*pageKey[R], len(uniqueSuffix))
	for i, item := range uniqueSuffix {
		key, ok := item.(*pageKey[R])
		if !ok || key == nil {
			return PageSpec[R]{}, planError("invalid_page_spec", fmt.Sprintf("uniqueSuffix[%d]", i), "must be a valid page key")
		}
		suffix[i] = key
		at := len(keys) - len(suffix) + i
		if at < 0 || keys[at] != key {
			return PageSpec[R]{}, planError("order_not_unique", "uniqueSuffix", "must match the terminal page keys")
		}
	}
	return PageSpec[R]{keys: append([]*pageKey[R](nil), keys...), uniqueSuffix: append([]*pageKey[R](nil), suffix...)}, nil
}

type PagePolicy struct{ DefaultLimit, MaxLimit int }

var DefaultPagePolicy = PagePolicy{DefaultLimit: 50, MaxLimit: 1000}

type PageRequest struct {
	Limit int
	After Cursor
}

type Page[R any] struct {
	Values  []R
	Next    Cursor
	HasMore bool
}

func copyPageValue[T any](value T) (any, error) {
	owned, _, err := adoptBind(value, false)
	if err != nil {
		return nil, err
	}
	return owned, nil
}

// pageKeyFields projects the per-key metadata a cursor envelope carries. The
// cursorcodec package writes these bytes back verbatim, and decodePageCursor
// compares what it reads against the same projection.
func pageKeyFields[R any](keys []*pageKey[R]) []cursorcodec.Field {
	fields := make([]cursorcodec.Field, len(keys))
	for i, key := range keys {
		fields[i] = cursorcodec.Field{
			Direction: uint8(key.direction),
			Nullable:  key.nullable,
			Nulls:     uint8(key.term.nulls),
			Codec:     key.codec,
		}
	}
	return fields
}
