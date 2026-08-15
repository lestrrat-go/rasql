package rasql_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every generated file this module writes is published by renaming a
// complete temporary file over its destination, and os.Rename is an atomic
// operation only on Unix. So the promise that a killed run leaves each file
// either its previous content or its new content is a Unix promise: on
// another platform an interrupted rename can leave the destination missing,
// which is neither. internal/genfile's package comment owns that limit, and
// generate.Plan.Commit states what it costs a whole commit.
//
// A page or a doc comment that repeats the promise without naming the
// platform is the failure this guard exists to catch. Nothing marks the
// packages Unix-only -- both build for windows/amd64 -- while the tests that
// pin the old-or-new behavior are guarded by //go:build unix and run nowhere
// else, so an unqualified promise is one no test on the platform it is read
// on ever checks.

// writeGuaranteeClaims are the phrases this module states that promise in,
// matched case-insensitively. They are the copies text search can find: a
// paraphrase shares none of these words and has to be caught by reading, so
// a passing run here is evidence about the copies and not a certificate that
// every restatement is qualified.
var writeGuaranteeClaims = []string{
	"previous content or its new content",
	"partial write",
	"old or new",
}

// writeGuaranteePlatform is the qualifier a block making one of those claims
// has to carry. Matched case-insensitively, so the "//go:build unix" a test
// file is gated by counts as naming it too.
const writeGuaranteePlatform = "unix"

// writeGuaranteeGuardFile is this file, which is skipped because it holds
// every claim phrase by definition. Skipping it by name rather than by
// content keeps the table above readable as plain strings.
const writeGuaranteeGuardFile = "write_guarantee_docs_test.go"

// claimMissingPlatform reports the first claim phrase block makes without
// naming the platform, and "" when block makes none or names it. A block is
// one comment group in a Go file and one blank-line-separated paragraph in a
// Markdown file: the unit a reader takes the claim and its qualifier from
// together.
func claimMissingPlatform(block string) string {
	lowered := strings.ToLower(block)
	if strings.Contains(lowered, writeGuaranteePlatform) {
		return ""
	}
	for _, claim := range writeGuaranteeClaims {
		if strings.Contains(lowered, claim) {
			return claim
		}
	}
	return ""
}

// TestWriteGuaranteeClaimsNameTheirPlatform reconciles every copy of the
// promise in the tree against the qualifier it needs. It reads the checked-in
// files rather than a fixture, so a copy added to a page or a doc comment
// anywhere in this module -- not only in the two packages that own the
// behavior -- is checked the moment it lands.
func TestWriteGuaranteeClaimsNameTheirPlatform(t *testing.T) {
	root, err := os.Getwd()
	require.NoError(t, err, "os.Getwd")

	var unqualified []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			// The same directories go itself passes over, so a scratch copy
			// of this repository under ".worktrees" is not read as a second
			// set of claims, plus nested modules, whose text is their own.
			if goAlwaysSkipsDirName(d.Name()) {
				return filepath.SkipDir
			}
			if info, err := os.Stat(filepath.Join(path, "go.mod")); err == nil && !info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == writeGuaranteeGuardFile {
			return nil
		}

		var found []string
		switch filepath.Ext(path) {
		case ".go":
			found, err = goClaimsMissingPlatform(path)
		case ".md":
			found, err = markdownClaimsMissingPlatform(path)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		for _, site := range found {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			unqualified = append(unqualified, rel+":"+site)
		}
		return nil
	})
	require.NoError(t, err, "walk %s", root)

	require.Emptyf(t, unqualified, "these blocks promise what an interrupted run leaves on disk without naming the platform that promise holds on:\n\t%s\nEach one is read on windows too, where the publishing rename is not atomic and the destination can be left missing. Name the platform in the same block, or point at internal/genfile's package comment, which owns the limit.", strings.Join(unqualified, "\n\t"))
}

// goClaimsMissingPlatform reports every comment group in a Go file that makes
// a claim without naming the platform, as "line: claim". Comments are read
// from the whole file rather than from doc comments alone, since a claim
// inside a function is read by the next person to change it.
func goClaimsMissingPlatform(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, group := range file.Comments {
		if claim := claimMissingPlatform(group.Text()); claim != "" {
			found = append(found, fmt.Sprintf("%d: %q", fset.Position(group.Pos()).Line, claim))
		}
	}
	return found, nil
}

// markdownClaimsMissingPlatform reports every paragraph of a Markdown file
// that makes a claim without naming the platform, as "line: claim".
func markdownClaimsMissingPlatform(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var found []string
	line := 1
	for _, paragraph := range strings.SplitAfter(string(data), "\n\n") {
		if claim := claimMissingPlatform(paragraph); claim != "" {
			found = append(found, fmt.Sprintf("%d: %q", line, claim))
		}
		line += strings.Count(paragraph, "\n")
	}
	return found, nil
}

// TestClaimMissingPlatformReadsAClaimAndItsQualifier pins what the guard
// answers for the shapes the checked-in tree cannot show it: a claim with no
// qualifier, and a qualifier that arrives in a different block than the claim
// it belongs to. Every sentence below is invented for this test and appears
// nowhere in this module, so no line here can be read as a report about real
// text.
func TestClaimMissingPlatformReadsAClaimAndItsQualifier(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block string
		want  string
	}{
		{
			name:  "an unqualified promise is caught",
			block: "A halted run leaves each output at its previous content or its new content.",
			want:  "previous content or its new content",
		},
		{
			name:  "the same promise qualified is accepted",
			block: "On Unix a halted run leaves each output at its previous content or its new content.",
			want:  "",
		},
		{
			name:  "a lowercase qualifier counts",
			block: "//go:build unix\n\nA halted run leaves each output old or new.",
			want:  "",
		},
		{
			name:  "the shorter spelling is caught too",
			block: "Nothing a halted run leaves behind is a partial write.",
			want:  "partial write",
		},
		{
			name:  "old or new is caught on its own",
			block: "Whatever is on disk afterwards is old or new.",
			want:  "old or new",
		},
		{
			name:  "prose about neither is left alone",
			block: "Each output keeps the permission bits it already carried.",
			want:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, claimMissingPlatform(tc.block))
		})
	}
}
