package store

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type member struct {
	ID    int64  `rasql:"id"`
	Name  string `rasql:"name"`
	Email string `rasql:"email"`
}

type project struct {
	ID       int64  `rasql:"id"`
	OwnerID  int64  `rasql:"owner_id"`
	Name     string `rasql:"name"`
	Archived bool   `rasql:"archived"`
}

type task struct {
	ID         int64  `rasql:"id"`
	ProjectID  int64  `rasql:"project_id"`
	AssigneeID int64  `rasql:"assignee_id"`
	Title      string `rasql:"title"`
	Status     string `rasql:"status"`
	Priority   int64  `rasql:"priority"`
}

type membersTable struct {
	rasql.Table[member]
	ID    query.Column
	Name  query.Column
	Email query.Column
}

func newMembersTable(table rasql.Table[member]) membersTable {
	return membersTable{
		Table: table,
		ID:    rasql.MustColumn(table, "id"),
		Name:  rasql.MustColumn(table, "name"),
		Email: rasql.MustColumn(table, "email"),
	}
}

type projectsTable struct {
	rasql.Table[project]
	ID       query.Column
	OwnerID  query.Column
	Name     query.Column
	Archived query.Column
}

func newProjectsTable(table rasql.Table[project]) projectsTable {
	return projectsTable{
		Table:    table,
		ID:       rasql.MustColumn(table, "id"),
		OwnerID:  rasql.MustColumn(table, "owner_id"),
		Name:     rasql.MustColumn(table, "name"),
		Archived: rasql.MustColumn(table, "archived"),
	}
}

type tasksTable struct {
	rasql.Table[task]
	ID         query.Column
	ProjectID  query.Column
	AssigneeID query.Column
	Title      query.Column
	Status     query.Column
	Priority   query.Column
}

func newTasksTable(table rasql.Table[task]) tasksTable {
	return tasksTable{
		Table:      table,
		ID:         rasql.MustColumn(table, "id"),
		ProjectID:  rasql.MustColumn(table, "project_id"),
		AssigneeID: rasql.MustColumn(table, "assignee_id"),
		Title:      rasql.MustColumn(table, "title"),
		Status:     rasql.MustColumn(table, "status"),
		Priority:   rasql.MustColumn(table, "priority"),
	}
}

var members = newMembersTable(rasql.MustTable[member](schema.Table{
	Name: "members",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "name", Type: schema.TypeText},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
	UniqueConstraints: []schema.UniqueConstraint{{
		Columns: []string{"email"},
	}},
}))

var projects = newProjectsTable(rasql.MustTable[project](schema.Table{
	Name: "projects",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "owner_id", Type: schema.TypeInteger},
		{Name: "name", Type: schema.TypeText},
		{Name: "archived", Type: schema.TypeBoolean, Default: "FALSE"},
	},
	PrimaryKey: []string{"id"},
	ForeignKeys: []schema.ForeignKey{{
		Columns:           []string{"owner_id"},
		ReferencedTable:   "members",
		ReferencedColumns: []string{"id"},
	}},
}))

var tasks = newTasksTable(rasql.MustTable[task](schema.Table{
	Name: "tasks",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "project_id", Type: schema.TypeInteger},
		{Name: "assignee_id", Type: schema.TypeInteger},
		{Name: "title", Type: schema.TypeText},
		{Name: "status", Type: schema.TypeText},
		{Name: "priority", Type: schema.TypeInteger},
	},
	PrimaryKey: []string{"id"},
	Checks: []schema.CheckConstraint{{
		Expression: "status IN ('todo', 'in_progress', 'done')",
	}},
	Indexes: []schema.Index{{
		Name:    "tasks_open_by_project",
		Columns: []string{"project_id", "status", "priority"},
	}},
	ForeignKeys: []schema.ForeignKey{
		{
			Columns:           []string{"project_id"},
			ReferencedTable:   "projects",
			ReferencedColumns: []string{"id"},
		},
		{
			Columns:           []string{"assignee_id"},
			ReferencedTable:   "members",
			ReferencedColumns: []string{"id"},
		},
	},
}))
