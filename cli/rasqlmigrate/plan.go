package rasqlmigrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/dsnredact"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

func formatChangePlan(plan changeplan.Plan) ([]byte, error) {
	operations, err := plan.TopologicalOperations()
	if err != nil {
		return nil, fmt.Errorf("resolve migration plan order: %w", err)
	}
	var output bytes.Buffer
	_, _ = fmt.Fprintf(&output, "plan\t%s\n", plan.ID())
	for index, operation := range operations {
		_, _ = fmt.Fprintf(&output, "operation\t%d\t%s\t%s\t%s\n", index, operation.ID(), operation.Kind(), operation.Transaction())
	}
	return output.Bytes(), nil
}

func runChangePlanCheck(args []string) error {
	flags := newFlagSet("plan check")
	file := flags.String("file", "", "serialized migration plan file")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("plan check accepts no positional arguments")
	}
	if *file == "" || *dialectName == "" || *dsn == "" {
		return errors.New("plan check requires -file, -dialect, and -dsn")
	}
	plan, err := changeplan.Read(*file)
	if err != nil {
		return fmt.Errorf("read migration plan: %w", err)
	}
	runner, cleanup, err := openChangePlanRunner(context.Background(), *dialectName, *dsn, *historyTable)
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	check, operationErr := runner.CheckChangePlan(context.Background(), plan)
	cleanupErr := cleanup()
	if err := errors.Join(operationErr, cleanupErr); err != nil {
		return dsnredact.Error(err, *dsn)
	}
	var output bytes.Buffer
	_, _ = fmt.Fprintf(&output, "plan\t%s\n", check.PlanID())
	_, _ = fmt.Fprintf(&output, "next-operation\t%d\n", check.NextOperationIndex())
	_, _ = fmt.Fprintf(&output, "catalog-digest\t%s\n", check.LastCatalogDigest())
	_, _ = fmt.Fprintf(&output, "complete\t%t\n", check.Complete())
	_, _ = commandOutput.Write(output.Bytes())
	return nil
}

func runChangePlanApply(file, dialectName, dsn, historyTable string) error {
	if file == "" || dialectName == "" || dsn == "" {
		return errors.New("apply -plan requires -plan, -dialect, and -dsn")
	}
	plan, err := changeplan.Read(file)
	if err != nil {
		return fmt.Errorf("read migration plan: %w", err)
	}
	runner, cleanup, err := openChangePlanRunner(context.Background(), dialectName, dsn, historyTable)
	if err != nil {
		return dsnredact.Error(err, dsn)
	}
	result, operationErr := runner.ApplyChangePlan(context.Background(), plan)
	cleanupErr := cleanup()
	if err := errors.Join(operationErr, cleanupErr); err != nil {
		return dsnredact.Error(err, dsn)
	}
	var output bytes.Buffer
	for index, operation := range result.CompletedOperations {
		_, _ = fmt.Fprintf(&output, "applied-operation\t%d\t%s\n", index, operation.ID())
	}
	_, _ = fmt.Fprintf(&output, "migration plan apply completed: %d applied\n", len(result.CompletedOperations))
	_, _ = commandOutput.Write(output.Bytes())
	return nil
}

func openChangePlanRunner(
	ctx context.Context,
	dialectName string,
	dsn string,
	historyTable string,
) (migrate.Runner, func() error, error) {
	if dialectName == "" || dsn == "" {
		return migrate.Runner{}, func() error { return nil }, errors.New("plan database commands require -dialect and -dsn")
	}
	dialectValue, err := migrationDialect(dialectName)
	if err != nil {
		return migrate.Runner{}, func() error { return nil }, err
	}
	driverName, err := driverForDialect(dialectValue.Name())
	if err != nil {
		return migrate.Runner{}, func() error { return nil }, err
	}
	database, err := openDatabase(driverName, dsn)
	if err != nil {
		return migrate.Runner{}, func() error { return nil }, fmt.Errorf("open database: %w", err)
	}
	cleanup := database.Close
	if err := database.PingContext(ctx); err != nil {
		return migrate.Runner{}, func() error { return nil }, errors.Join(fmt.Errorf("connect to database: %w", err), cleanup())
	}
	var runner migrate.Runner
	if historyTable == "" {
		runner, err = migrate.New(database, dialectValue)
	} else {
		runner, err = migrate.NewWithHistoryTable(database, dialectValue, historyTable)
	}
	if err != nil {
		return migrate.Runner{}, func() error { return nil }, errors.Join(err, cleanup())
	}
	return runner, cleanup, nil
}
