package rasqlgen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/lestrrat-go/rasql/internal/modroot"
)

// runCheck reports whether generated output is current, without writing anything. With -dsn or
// -scratch it regenerates in memory against a live database and compares; otherwise it reads
// rasql.sum and recomputes every line from the working tree, consulting no database at all.
func (c command) runCheck(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "check")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "connection string; required unless -scratch is set for SQLite")
	scratch := flags.Bool("scratch", false, "build a throwaway database from -dsn, apply migrations, and check the result")
	timeout := flags.Duration("timeout", defaultGenerateTimeout, "check timeout")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *dsn != "" || *scratch {
		if err := c.runCheckLive(*configPath, settings, *dsn, *scratch, *timeout); err != nil {
			return fmt.Errorf("check: %w", err)
		}
		return nil
	}
	if err := c.runCheckOffline(*configPath, settings); err != nil {
		return fmt.Errorf("check: %w", err)
	}
	return nil
}

// runCheckLive performs steps 1 through 7 of design section 4.1 against dsn (or a database
// scratch-built from it), the way generate does, then recomputes rasql.sum from the live result
// and compares it against the recorded one, and finally generate.Plan.Check -- the in-memory
// comparison against what is on disk. The sum comparison is checked first and reports its
// difference by group (dialect, profile, settings, migrations, queries, outputs) whenever the
// server, the migrations, or the query and output files it can see differ from what generate last
// recorded; generate.Plan.Check catches the one thing the sum comparison cannot, because it never
// reads the working tree back: a generated file hand-edited since the last generate, with the
// database and every configured input otherwise unchanged. So a passing live check also leaves
// the offline check passing.
func (c command) runCheckLive(configPath string, cfg config, dsn string, scratch bool, timeout time.Duration) error {
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	lg, err := c.prepareLiveGeneration(ctx, configPath, cfg, dsn, scratch)
	if err != nil {
		return err
	}
	sumBytes, err := os.ReadFile(filepath.Join(lg.moduleRoot, filepath.FromSlash(lg.sumRelPath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: rasql.sum does not exist; run rasql codegen generate", generate.ErrStale)
		}
		return err
	}
	recorded, err := gensum.Parse(sumBytes)
	if err != nil {
		return fmt.Errorf("parse rasql.sum: %w", err)
	}
	current, err := buildGensumFile(cfg, lg.configDir, lg.moduleRoot, canonicalDialectName(cfg.Dialect), lg.result.Profile.ID, lg.result.Migrations, lg.plan.Files(), lg.outputAbs)
	if err != nil {
		return err
	}
	if diffs := gensum.Compare(recorded, current); len(diffs) != 0 {
		return fmt.Errorf("%w: %s", generate.ErrStale, formatGensumDiffs(diffs))
	}
	if err := lg.plan.Check(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "%s matches the database\n", cfg.Output)
	return nil
}

// runCheckOffline reads rasql.sum and recomputes every group -- settings, migrations, queries,
// outputs -- from the working tree, consulting no database. dialect and profile are carried
// forward from the recorded sum unchanged rather than compared, because neither is something an
// offline run can observe; a changed engine major surfaces through check -dsn instead, which
// regenerates rasql.sum with whatever the server reports.
func (c command) runCheckOffline(configPath string, cfg config) error {
	if cfg.Output == "" {
		return errors.New("config requires output")
	}
	configDir, err := moduleRootForConfig(configPath)
	if err != nil {
		return err
	}
	moduleRoot := modroot.From(configDir)
	if moduleRoot == "" {
		moduleRoot = configDir
	}
	outputAbs, err := filepath.Abs(filepath.Join(configDir, filepath.FromSlash(cfg.Output)))
	if err != nil {
		return fmt.Errorf("resolve output: %w", err)
	}
	sumPath := filepath.Join(outputAbs, "rasql.sum")
	sumBytes, err := os.ReadFile(sumPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: rasql.sum does not exist; run rasql codegen generate (no database was consulted)", generate.ErrStale)
		}
		return err
	}
	recorded, err := gensum.Parse(sumBytes)
	if err != nil {
		return fmt.Errorf("parse rasql.sum: %w", err)
	}

	settings, err := settingsDigest(cfg)
	if err != nil {
		return err
	}
	migrationsDir := ""
	if cfg.Migrations != "" {
		migrationsDir, err = rebaseToModuleRoot(configDir, moduleRoot, cfg.Migrations)
		if err != nil {
			return fmt.Errorf("resolve migrations: %w", err)
		}
	}
	currentMigrations, err := currentMigrationEntries(moduleRoot, migrationsDir)
	if err != nil {
		return fmt.Errorf("load migrations (no database was consulted): %w", err)
	}
	currentQueries, err := queryEntries(configDir, moduleRoot, cfg.Queries, true)
	if err != nil {
		return err
	}
	currentOutputs := currentOutputEntries(outputAbs, recorded.Outputs)

	current := gensum.File{
		Dialect: recorded.Dialect, Profile: recorded.Profile, Settings: "sha256:" + settings,
		Migrations: currentMigrations, Queries: currentQueries, Outputs: currentOutputs,
	}
	if diffs := gensum.Compare(recorded, current); len(diffs) != 0 {
		return fmt.Errorf("%w: %s (no database was consulted)", generate.ErrStale, formatGensumDiffs(diffs))
	}
	_, _ = fmt.Fprintf(c.output, "%s is up to date; no database was consulted\n", cfg.Output)
	return nil
}
