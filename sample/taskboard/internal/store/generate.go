//go:generate go -C ../../../.. run ./cmd/rasqlmigrate apply -dir sample/taskboard/migrations/sqlite -dialect sqlite -dsn sample/taskboard/internal/store/.taskboard-schema.db
//go:generate go -C ../../../.. run ./cmd/rasqlgen schema -dsn sample/taskboard/internal/store/.taskboard-schema.db -dialect sqlite -table members -table projects -table tasks -package store -output sample/taskboard/internal/store/schema_gen.go

package store
