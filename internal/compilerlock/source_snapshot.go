package compilerlock

import "github.com/lestrrat-go/rasql/internal/sourcefile"

// SourceFileFrom converts a sourcefile.SourceFileSnapshot into the SourceFile record the lock
// encodes. The snapshot type itself lives in internal/sourcefile; this is the only place that
// still needs to know how a snapshot becomes a lock record.
func SourceFileFrom(s sourcefile.SourceFileSnapshot) SourceFile {
	return SourceFile{Path: s.Path(), SHA256: s.SHA256()}
}
