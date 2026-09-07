package rasql

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

type Cardinality uint8

const (
	Many Cardinality = iota + 1
	AtMostOne
	ExactlyOne
)

type NativeStatement struct {
	Engine string
	SQL    string
	Args   []NativeArgument
}

type NativeArgument struct {
	Value any
	Codec string
}

type nativeQueryPlan struct {
	engine    string
	statement stmt.Statement
}

type queryResultRequirement struct {
	cardinality Cardinality
	emptyErr    error
}

func Native[R any](statement NativeStatement, projection Projection[R], cardinality Cardinality) (Query[R], error) {
	engine := strings.TrimSpace(statement.Engine)
	if engine == "" {
		return Query[R]{}, planError("invalid_projection", "native.engine", "must not be empty")
	}
	if strings.TrimSpace(statement.SQL) == "" {
		return Query[R]{}, planError("invalid_projection", "native.sql", "must not be empty")
	}
	if cardinality < Many || cardinality > ExactlyOne {
		return Query[R]{}, planError("invalid_projection", "native.cardinality", "invalid cardinality")
	}
	if err := projection.Validate(); err != nil {
		return Query[R]{}, err
	}
	args := make([]any, len(statement.Args))
	for i, arg := range statement.Args {
		if arg.Codec != "" && !codecPattern.MatchString(arg.Codec) {
			return Query[R]{}, planError("invalid_schema", fmt.Sprintf("native.args[%d].codec", i), "malformed codec identifier")
		}
		snapshot, copier, err := adoptBind(arg.Value, true)
		if err != nil {
			return Query[R]{}, planError("unsnapshotable_bind", fmt.Sprintf("native.args[%d]", i), err.Error())
		}
		args[i] = bindToken{id: bindID(atomic.AddUint64(&nextBindID, 1)), value: snapshot, codec: arg.Codec, copy: copier}
	}
	return Query[R]{
		plan:              QueryPlan{native: &nativeQueryPlan{engine: engine, statement: stmt.New(sqltext.Text(statement.SQL), args...)}, projection: cloneItems(projection.items)},
		projection:        projection,
		resultRequirement: queryResultRequirement{cardinality: cardinality, emptyErr: nativeEmptyError(cardinality)},
	}, nil
}

func (p Projection[R]) Validate() error {
	if len(p.items) == 0 || p.decoder == nil {
		return planError("invalid_projection", "projection", "must not be zero")
	}
	return nil
}

func (q Query[R]) nativeInfo() (string, Cardinality, bool) {
	if q.plan.native == nil {
		return "", q.resultRequirement.cardinality, false
	}
	return q.plan.native.engine, q.resultRequirement.cardinality, true
}

func nativeEmptyError(cardinality Cardinality) error {
	if cardinality == ExactlyOne {
		return ErrNoRows
	}
	return nil
}

func cloneNativePlan(native *nativeQueryPlan) *nativeQueryPlan {
	if native == nil {
		return nil
	}
	args := native.statement.Args()
	return &nativeQueryPlan{engine: native.engine, statement: stmt.New(sqltext.Text(native.statement.SQL()), args...)}
}
