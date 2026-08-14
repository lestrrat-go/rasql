// BEGIN(go_generate)
//go:generate go -C ../../../.. run ./cmd/rasqlmigrate apply -dir sample/taskboard/migrations/sqlite -dialect sqlite -dsn sample/taskboard/internal/store/.taskboard-schema.db
//go:generate go -C ../../../.. run ./cmd/rasqlgen schema -dsn sample/taskboard/internal/store/.taskboard-schema.db -dialect sqlite -package store -output sample/taskboard/internal/store
// END(go_generate)

package store
