package rasqlgen

import "errors"

// runGenerate renders the checked schema lock through the compact emitter.
// A live or migration-backed refresh belongs to schema update, which records
// the source evidence before publishing generated files.
func (c command) runGenerate(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "generate")
	configPath := flags.String("config", "", "settings file")
	check := flags.Bool("check", false, "report whether generated files are current instead of writing them")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if settings.Engine == nil || settings.Schema == nil {
		return errors.New("generate: config requires engine and schema; run schema update to create a lock")
	}
	return c.runOfflineGenerate(settings, *configPath, *check)
}
