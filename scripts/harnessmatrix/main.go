// harnessmatrix writes the support matrix from internal/harness into README.md,
// docs/HARNESS-MATRIX.md and docs/harness-matrix.json. Run from the repo root:
//
//	go run ./scripts/harnessmatrix          # regenerate
//	go run ./scripts/harnessmatrix -check   # exit 1 if the checked-in copies drift
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/liveshortly/live/internal/harness"
)

func main() {
	check := flag.Bool("check", false, "report drift instead of writing")
	flag.Parse()
	drifted, err := harness.Sync(".", !*check)
	if err != nil {
		fmt.Fprintln(os.Stderr, "harnessmatrix:", err)
		os.Exit(2)
	}
	for _, f := range drifted {
		if *check {
			fmt.Fprintf(os.Stderr, "%s is out of date: run go run ./scripts/harnessmatrix\n", f)
		} else {
			fmt.Println("wrote", f)
		}
	}
	if *check && len(drifted) > 0 {
		os.Exit(1)
	}
}
