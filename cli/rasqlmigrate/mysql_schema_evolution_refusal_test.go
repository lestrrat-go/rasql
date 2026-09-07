package rasqlmigrate

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunMySQLRefusalDoesNotPublishOutput(t *testing.T) {
	cases := []struct {
		name, baseline, target, want string
	}{
		{
			name:     "anonymous unique replacement",
			baseline: "CREATE TABLE `facts` (`id` bigint NOT NULL, `parent_id` bigint NOT NULL, UNIQUE (`id`));\n",
			target:   "CREATE TABLE `facts` (`id` bigint NOT NULL, `parent_id` bigint NOT NULL, UNIQUE (`parent_id`));\n",
			want:     "mysql schema diff: table facts constraints changed: anonymous UNIQUE replacement is unsupported",
		},
		{
			name:     "foreign key match",
			baseline: "CREATE TABLE `parent` (`id` bigint PRIMARY KEY); CREATE TABLE `facts` (`parent_id` bigint);\n",
			target:   "CREATE TABLE `parent` (`id` bigint PRIMARY KEY); CREATE TABLE `facts` (`parent_id` bigint, CONSTRAINT `fk_facts` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`) MATCH FULL);\n",
			want:     `parse target desired schema: mysql schema source "tables/facts.sql": unsupported FOREIGN KEY MATCH clause`,
		},
		{
			name:     "unresolved required column",
			baseline: "CREATE TABLE `facts` (`id` bigint PRIMARY KEY);\n",
			target:   "CREATE TABLE `facts` (`id` bigint PRIMARY KEY, `email` varchar(20) NOT NULL);\n",
			want:     "migrate diff: unresolved decisions: backfill_mysql_facts_email",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			baseline := filepath.Join(root, "baseline")
			target := filepath.Join(root, "target")
			writeTestSchema(t, baseline, "tables/facts.sql", test.baseline)
			writeTestSchema(t, target, "tables/facts.sql", test.target)
			output := filepath.Join(root, "new", "001_refused")
			commandOutput := setCommandOutput(t)
			err := run([]string{"diff", "-dialect", "mysql", "-from", baseline, "-to", target, "-output", output})
			require.EqualError(t, err, test.want)
			require.Empty(t, commandOutput.String())
			assertNoCLIOutputPath(t, filepath.Dir(output), filepath.Base(output))
		})
	}
}
