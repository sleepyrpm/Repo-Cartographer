// fixture: part of repo-cartographer's testdata/sample-repo, not a real project
package main

import (
	"fmt"

	"example.com/pulse/internal/digest"
)

func main() {
	fmt.Println(digest.Line(10, 0.42))
}
