package rasql

import "github.com/lestrrat-go/rasql/exec"

// RowSource is the row-reading surface shared by database/sql and owned rows.
type RowSource = exec.RowSource
