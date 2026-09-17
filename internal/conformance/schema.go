package conformance

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

const (
	SchemaDigest = "d4-schema-v2"
	SeedDigest   = "d4-seed-v2"
)

var schemaStatements = []string{
	`CREATE TABLE members (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL)`,
	`CREATE TABLE projects (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL)`,
	`CREATE TABLE tasks (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, assignee_id INTEGER, title VARCHAR(128) NOT NULL, is_open BOOLEAN NOT NULL DEFAULT TRUE, due_on DATE, created_at TIMESTAMP NOT NULL DEFAULT '2024-01-01T00:00:00Z', FOREIGN KEY (project_id) REFERENCES projects(id), FOREIGN KEY (assignee_id) REFERENCES members(id))`,
	`CREATE TABLE task_labels (task_id INTEGER NOT NULL, label VARCHAR(32) NOT NULL, PRIMARY KEY (task_id, label), FOREIGN KEY (task_id) REFERENCES tasks(id))`,
	`CREATE INDEX tasks_project_open_id ON tasks(project_id, is_open, id)`,
	`CREATE INDEX tasks_assignee ON tasks(assignee_id, id)`,
}

type SeedRow struct {
	ID         int64
	ProjectID  int64
	AssigneeID *int64
	Title      string
	Open       bool
	Rank       int64
	DueOn      *string
	CreatedAt  string
}

func SchemaStatements() []string { return append([]string(nil), schemaStatements...) }

func schemaStatementsForEngine(engine string) []string {
	if engine != "mysql" {
		return SchemaStatements()
	}
	statements := SchemaStatements()
	statements[2] = strings.Replace(statements[2], "TIMESTAMP NOT NULL DEFAULT '2024-01-01T00:00:00Z'", "DATETIME NOT NULL DEFAULT '2024-01-01 00:00:00'", 1)
	return statements
}
func SeedRows() []SeedRow {
	rows := make([]SeedRow, 0, 3500)
	for project := int64(1); project <= 500; project++ {
		for rank := int64(0); rank < 7; rank++ {
			id := (project-1)*7 + rank + 1
			var assigneePtr *int64
			if id%11 != 0 {
				assignee := id
				assigneePtr = &assignee
			}
			var dueOn *string
			if rank%2 == 0 {
				value := fmt.Sprintf("2024-01-%02d", rank+1)
				dueOn = &value
			}
			rows = append(rows, SeedRow{ID: id, ProjectID: project, AssigneeID: assigneePtr,
				Title: fmt.Sprintf("task-%04d", id), Open: rank < 6, Rank: rank,
				DueOn: dueOn, CreatedAt: "2024-01-01T00:00:00Z"})
		}
	}
	return rows
}
func SeedDatabase(ctx context.Context, db *sql.DB) error {
	return SeedDatabaseForEngine(ctx, db, "sqlite")
}
func SeedDatabaseForEngine(ctx context.Context, db *sql.DB, engine string) error {
	if db == nil {
		return fmt.Errorf("conformance database is nil")
	}
	for _, statement := range schemaStatementsForEngine(engine) {
		if _, err := db.ExecContext(ctx, BindSQL(engine, statement)); err != nil {
			return fmt.Errorf("conformance schema: %w", err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("conformance seed transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	members, err := tx.PrepareContext(ctx, BindSQL(engine, "INSERT INTO members(id, name) VALUES (?, ?)"))
	if err != nil {
		return fmt.Errorf("conformance members statement: %w", err)
	}
	defer func() { _ = members.Close() }()
	for id := int64(1); id <= 3500; id++ {
		if _, err := members.ExecContext(ctx, id, fmt.Sprintf("member-%02d", id)); err != nil {
			return fmt.Errorf("conformance members seed: %w", err)
		}
	}
	projects, err := tx.PrepareContext(ctx, BindSQL(engine, "INSERT INTO projects(id, name) VALUES (?, ?)"))
	if err != nil {
		return fmt.Errorf("conformance projects statement: %w", err)
	}
	defer func() { _ = projects.Close() }()
	for id := int64(1); id <= 500; id++ {
		if _, err := projects.ExecContext(ctx, id, fmt.Sprintf("project-%03d", id)); err != nil {
			return fmt.Errorf("conformance projects seed: %w", err)
		}
	}
	tasks, err := tx.PrepareContext(ctx, BindSQL(engine, "INSERT INTO tasks(id, project_id, assignee_id, title, is_open, due_on, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)"))
	if err != nil {
		return fmt.Errorf("conformance tasks statement: %w", err)
	}
	defer func() { _ = tasks.Close() }()
	labels, err := tx.PrepareContext(ctx, BindSQL(engine, "INSERT INTO task_labels(task_id, label) VALUES (?, ?)"))
	if err != nil {
		return fmt.Errorf("conformance labels statement: %w", err)
	}
	defer func() { _ = labels.Close() }()
	for _, row := range SeedRows() {
		createdAt := row.CreatedAt
		if engine == "mysql" {
			createdAt = strings.Replace(createdAt, "T", " ", 1)
			createdAt = strings.TrimSuffix(createdAt, "Z")
		}
		if _, err := tasks.ExecContext(ctx, row.ID, row.ProjectID, row.AssigneeID, row.Title, row.Open, row.DueOn, createdAt); err != nil {
			return fmt.Errorf("conformance tasks seed: %w", err)
		}
		if _, err := labels.ExecContext(ctx, row.ID, labelFor(row)); err != nil {
			return fmt.Errorf("conformance labels seed: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("conformance seed commit: %w", err)
	}
	return nil
}
func BindSQL(engine, statement string) string {
	if engine != "postgresql" {
		return statement
	}
	var builder strings.Builder
	argument := 0
	for _, character := range statement {
		if character == '?' {
			argument++
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(argument))
		} else {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}
func labelFor(row SeedRow) string {
	if row.Open {
		return "open"
	}
	return "closed"
}
// canonicalSeedRows builds the seed the portable signature describes, in the order
// SeedDatabaseForEngine inserts it: every member, then every project, then each task followed by
// its own label. LoadPortableSignature calls this and fills SignatureDocument.Seed.Rows with the
// result, so the rows the signature validates are the rows this package actually writes.
//
// testdata/portable-signature.json used to carry all 11,000 of these rows as JSON, a second copy
// of what the loops below produce. Nothing regenerated that copy, and portableSeedSHA256 still
// pins the same digest, so a drift between the two representations fails signature validation.
//
// Every integer is a float64 because validateSignatureValue type-checks integer columns as
// float64, matching what encoding/json produced when these rows were decoded from a file.
func canonicalSeedRows() []SignatureRow {
	rows := make([]SignatureRow, 0, 11000)
	for id := int64(1); id <= 3500; id++ {
		rows = append(rows, SignatureRow{Table: "members", Values: []any{float64(id), fmt.Sprintf("member-%02d", id)}})
	}
	for id := int64(1); id <= 500; id++ {
		rows = append(rows, SignatureRow{Table: "projects", Values: []any{float64(id), fmt.Sprintf("project-%03d", id)}})
	}
	for _, row := range SeedRows() {
		var assignee any
		if row.AssigneeID != nil {
			assignee = float64(*row.AssigneeID)
		}
		var dueOn any
		if row.DueOn != nil {
			dueOn = *row.DueOn
		}
		rows = append(rows, SignatureRow{Table: "tasks", Values: []any{
			float64(row.ID), float64(row.ProjectID), assignee, row.Title, row.Open, dueOn, row.CreatedAt,
		}})
		rows = append(rows, SignatureRow{Table: "task_labels", Values: []any{float64(row.ID), labelFor(row)}})
	}
	return rows
}

func SchemaSQLDigest() string { return DigestSQL(schemaStatements...) }
func SeedDigestValue() string {
	parts := []string{strings.Join(schemaStatements, "\n")}
	for _, row := range SeedRows() {
		assignee := "null"
		if row.AssigneeID != nil {
			assignee = fmt.Sprint(*row.AssigneeID)
		}
		dueOn := "null"
		if row.DueOn != nil {
			dueOn = *row.DueOn
		}
		parts = append(parts, fmt.Sprintf("%d|%d|%s|%s|%t|%d|%s|%s", row.ID, row.ProjectID, assignee, row.Title, row.Open, row.Rank, dueOn, row.CreatedAt))
	}
	return DigestParts(parts...)
}
