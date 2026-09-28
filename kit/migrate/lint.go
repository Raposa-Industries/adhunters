package migrate

import (
	"fmt"
	"regexp"
	"strings"
)

// Finding is one problem Lint found in a migration.
type Finding struct {
	Version int
	Name    string
	Problem string
}

func (f Finding) String() string {
	return fmt.Sprintf("%04d_%s: %s", f.Version, f.Name, f.Problem)
}

var (
	lineComment   = regexp.MustCompile(`--[^\n]*`)
	createIndex   = regexp.MustCompile(`(?i)\bcreate\s+(unique\s+)?index\b`)
	concurrently  = regexp.MustCompile(`(?i)\bcreate\s+(unique\s+)?index\s+concurrently\b`)
	destructive   = regexp.MustCompile(`(?i)\b(drop\s+(table|column|view|schema|function)|rename\s+(to|column)|alter\s+column\s+\S+\s+(set\s+data\s+)?type)\b`)
	decisionLine  = regexp.MustCompile(`(?m)^--\s*decision:\s*decisions/\S+`)
	newTableIndex = regexp.MustCompile(`(?i)--\s*lint:\s*new-table`)
)

// Lint refuses the changes that lock busy tables or lose data without a
// decision on record:
//
//   - CREATE INDEX without CONCURRENTLY, unless the file says
//     "-- lint: new-table" (an index on a table created in the same file is
//     safe, since nothing reads it yet).
//   - CREATE INDEX CONCURRENTLY outside a "-- migrate: no-transaction" file.
//   - DROP, RENAME or a column type change without a
//     "-- decision: decisions/NNNN-….md" line. Those go expand, switch,
//     contract, and the decision file says which step this is.
func Lint(migs []Migration) []Finding {
	var out []Finding
	for _, m := range migs {
		code := lineComment.ReplaceAllString(m.SQL, "")
		add := func(p string) { out = append(out, Finding{m.Version, m.Name, p}) }

		if n := len(createIndex.FindAllString(code, -1)); n > len(concurrently.FindAllString(code, -1)) && !newTableIndex.MatchString(m.SQL) {
			add("CREATE INDEX without CONCURRENTLY locks the table; use CONCURRENTLY in a no-transaction file, or mark '-- lint: new-table'")
		}
		if concurrently.MatchString(code) && !m.NoTx {
			add("CREATE INDEX CONCURRENTLY cannot run in a transaction; start the file with '" + noTxMarker + "'")
		}
		if destructive.MatchString(code) && !decisionLine.MatchString(m.SQL) {
			add(fmt.Sprintf("%q needs a '-- decision: decisions/NNNN-….md' line", strings.TrimSpace(destructive.FindString(code))))
		}
	}
	return out
}
