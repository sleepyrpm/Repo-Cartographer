// fixture: part of repo-cartographer's testdata/sample-repo, not a real project
package digest

import "fmt"

// Line renders one digest line.
func Line(rows int, seconds float64) string {
	return fmt.Sprintf("%d rows in %.2fs", rows, seconds)
}
