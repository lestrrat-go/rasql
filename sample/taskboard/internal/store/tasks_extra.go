package store

import "github.com/lestrrat-go/rasql/query"

// Open is the predicate that selects the tasks the page shows. It is a
// method on the generated table type, which Go permits only from inside the
// package that declares that type, so this file has to live beside the
// generated ones. A regenerating run leaves it alone: the generator writes
// the files it names and nothing else.
func (t TasksTable) Open() query.Expression {
	return query.Equal(t.IsOpen(), query.Bind(true))
}
