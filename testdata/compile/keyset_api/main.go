package keysetfixture

import (
	"database/sql/driver"

	"github.com/lestrrat-go/rasql"
)

type DomainID string

type domainCodec struct{}

func (domainCodec) Encode(value any) (driver.Value, error) { return string(value.(DomainID)), nil }
func (domainCodec) Decode(value any, destination any) error {
	*destination.(*DomainID) = DomainID(value.(string))
	return nil
}
func (domainCodec) EncodeCursor(value any) ([]byte, error) { return []byte(value.(DomainID)), nil }
func (domainCodec) DecodeCursor(value []byte) (any, error) { return DomainID(string(value)), nil }

var _ rasql.ValueCodec = domainCodec{}
var _ rasql.CursorValueCodec = domainCodec{}

func Use(key rasql.Expr[DomainID], row func(struct{}) DomainID) rasql.PageKey[struct{}] {
	return rasql.AscKey[struct{}](key, row)
}
