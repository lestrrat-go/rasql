package main

import (
	"context"
	"database/sql"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

type userRow struct{ ID int64 }
type taskRow struct{ ID, UserID int64 }
type labelRow struct{ ID int64 }
type linkRow struct{ UserID, LabelID int64 }
type employeeRow struct{ ID, ManagerID int64 }

type userGraph struct {
	Tasks  rasql.LoadedMany[taskGraph]
	Labels rasql.LoadedMany[labelGraph]
	Owner  rasql.LoadedOne[taskGraph]
}
type taskGraph struct{ ID int64 }
type labelGraph struct{ ID int64 }
type employeeGraph struct {
	Reports rasql.LoadedMany[employeeGraph]
}

type userDecoder struct{ schema rasql.ResultSchema }

func (d userDecoder) ResultSchema() rasql.ResultSchema                 { return d.schema }
func (userDecoder) Presence() []rasql.Presence                         { return nil }
func (d userDecoder) DecodeRow(s rasql.ScanSource, row *userRow) error { return s.Scan(&row.ID) }

type taskDecoder struct{ schema rasql.ResultSchema }

func (d taskDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (taskDecoder) Presence() []rasql.Presence         { return nil }
func (d taskDecoder) DecodeRow(s rasql.ScanSource, row *taskRow) error {
	return s.Scan(&row.ID, &row.UserID)
}

type labelDecoder struct{ schema rasql.ResultSchema }

func (d labelDecoder) ResultSchema() rasql.ResultSchema                  { return d.schema }
func (labelDecoder) Presence() []rasql.Presence                          { return nil }
func (d labelDecoder) DecodeRow(s rasql.ScanSource, row *labelRow) error { return s.Scan(&row.ID) }

type linkDecoder struct{ schema rasql.ResultSchema }

func (d linkDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (linkDecoder) Presence() []rasql.Presence         { return nil }
func (d linkDecoder) DecodeRow(s rasql.ScanSource, row *linkRow) error {
	return s.Scan(&row.UserID, &row.LabelID)
}

type employeeDecoder struct{ schema rasql.ResultSchema }

func (d employeeDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (employeeDecoder) Presence() []rasql.Presence         { return nil }
func (d employeeDecoder) DecodeRow(s rasql.ScanSource, row *employeeRow) error {
	return s.Scan(&row.ID, &row.ManagerID)
}

func intSchema(names ...string) rasql.ResultSchema {
	columns := make([]rasql.ResultColumn, len(names))
	for i, name := range names {
		columns[i] = rasql.ResultColumn{Name: name, Type: schema.IntegerType{}}
	}
	result, err := rasql.NewResultSchema(columns...)
	if err != nil {
		panic(err)
	}
	return result
}

func main() {
	users := rasql.MustReadTableOf[userRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	tasks := rasql.MustReadTableOf[taskRow](schema.TableDef{Name: "tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}}})
	labels := rasql.MustReadTableOf[labelRow](schema.TableDef{Name: "labels", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	links := rasql.MustReadTableOf[linkRow](schema.TableDef{Name: "user_labels", Columns: []schema.ColumnDef{{Name: "user_id", Type: schema.IntegerType{}}, {Name: "label_id", Type: schema.IntegerType{}}}})
	employees := rasql.MustReadTableOf[employeeRow](schema.TableDef{Name: "employees", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "manager_id", Type: schema.IntegerType{}}}})
	ur, _ := rasql.SourceOf(users, "u")
	tr, _ := rasql.SourceOf(tasks, "t")
	lr, _ := rasql.SourceOf(labels, "l")
	jr, _ := rasql.SourceOf(links, "j")
	er, _ := rasql.SourceOf(employees, "e")
	uid, _ := rasql.BindColumn[userRow, int64](ur, "id", "")
	tid, _ := rasql.BindColumn[taskRow, int64](tr, "id", "")
	tuid, _ := rasql.BindColumn[taskRow, int64](tr, "user_id", "")
	lid, _ := rasql.BindColumn[labelRow, int64](lr, "id", "")
	juid, _ := rasql.BindColumn[linkRow, int64](jr, "user_id", "")
	jlid, _ := rasql.BindColumn[linkRow, int64](jr, "label_id", "")
	eid, _ := rasql.BindColumn[employeeRow, int64](er, "id", "")
	emanager, _ := rasql.BindColumn[employeeRow, int64](er, "manager_id", "")
	up := rasql.Select(ur.Source(), mustProjection([]rasql.ProjectionItem{rasql.Item("id", uid.Expr(), schema.IntegerType{}, "")}, userDecoder{schema: intSchema("id")}))
	tp := rasql.Select(tr.Source(), mustProjection([]rasql.ProjectionItem{rasql.Item("id", tid.Expr(), schema.IntegerType{}, ""), rasql.Item("user_id", tuid.Expr(), schema.IntegerType{}, "")}, taskDecoder{schema: intSchema("id", "user_id")}))
	lp := rasql.Select(lr.Source(), mustProjection([]rasql.ProjectionItem{rasql.Item("id", lid.Expr(), schema.IntegerType{}, "")}, labelDecoder{schema: intSchema("id")}))
	_ = rasql.Select(jr.Source(), mustProjection([]rasql.ProjectionItem{rasql.Item("user_id", juid.Expr(), schema.IntegerType{}, ""), rasql.Item("label_id", jlid.Expr(), schema.IntegerType{}, "")}, linkDecoder{schema: intSchema("user_id", "label_id")}))
	ep := rasql.Select(er.Source(), mustProjection([]rasql.ProjectionItem{rasql.Item("id", eid.Expr(), schema.IntegerType{}, ""), rasql.Item("manager_id", emanager.Expr(), schema.IntegerType{}, "")}, employeeDecoder{schema: intSchema("id", "manager_id")}))
	uk, _ := rasql.NewGraphKey(rasql.KeyPart(uid, func(r userRow) int64 { return r.ID }))
	tk, _ := rasql.NewGraphKey(rasql.KeyPart(tuid, func(r taskRow) int64 { return r.UserID }))
	_, _ = rasql.NewGraphKey(rasql.KeyPart(tid, func(r taskRow) int64 { return r.ID }))
	lk, _ := rasql.NewGraphKey(rasql.KeyPart(lid, func(r labelRow) int64 { return r.ID }))
	ju, _ := rasql.NewGraphKey(rasql.KeyPart(juid, func(r linkRow) int64 { return r.UserID }))
	jl, _ := rasql.NewGraphKey(rasql.KeyPart(jlid, func(r linkRow) int64 { return r.LabelID }))
	ek, _ := rasql.NewGraphKey(rasql.KeyPart(eid, func(r employeeRow) int64 { return r.ID }))
	mk, _ := rasql.NewGraphKey(rasql.KeyPart(emanager, func(r employeeRow) int64 { return r.ManagerID }))
	tasksPlan, _ := rasql.NewGraphPlan(tp, func(r taskRow) taskGraph { return taskGraph{ID: r.ID} })
	labelsPlan, _ := rasql.NewGraphPlan(lp, func(r labelRow) labelGraph { return labelGraph{ID: r.ID} })
	employeesPlan, _ := rasql.NewGraphPlan(ep, func(r employeeRow) employeeGraph { return employeeGraph{} })
	tasksEdge, _ := rasql.HasMany("tasks", uk, tk, tasksPlan, rasql.EdgeOptions{}, func(g *userGraph, v rasql.LoadedMany[taskGraph]) { g.Tasks = v })
	ownerEdge, _ := rasql.HasOne("owner", uk, tk, tasksPlan, rasql.EdgeOptions{}, func(g *userGraph, v rasql.LoadedOne[taskGraph]) { g.Owner = v })
	labelsEdge, _ := rasql.ManyThrough("labels", uk, ju, jl, lk, jr.Source(), labelsPlan, rasql.EdgeOptions{}, func(g *userGraph, v rasql.LoadedMany[labelGraph]) { g.Labels = v })
	_, _ = rasql.NewGraphPlan(up, func(r userRow) userGraph { return userGraph{} }, tasksEdge, ownerEdge, labelsEdge)
	_, _ = rasql.HasMany("reports", mk, ek, employeesPlan, rasql.EdgeOptions{}, func(g *employeeGraph, v rasql.LoadedMany[employeeGraph]) { g.Reports = v })
	var executor rasql.Executor
	_, _ = rasql.LoadGraph(context.Background(), executor, employeesPlan)
	var _ rasql.Executor = compileExecutor{}
}

func mustProjection[R any](items []rasql.ProjectionItem, decoder rasql.RowDecoder[R]) rasql.Projection[R] {
	projection, err := rasql.NewProjection(items, decoder)
	if err != nil {
		panic(err)
	}
	return projection
}

type compileExecutor struct{}

func (compileExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (compileExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return nil, nil
}
func (compileExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) { return nil, nil }
