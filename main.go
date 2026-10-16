// goletter is a small terminal UI for sending newsletters with Amazon SES.
//
// Usage: goletter [dir]
//
// The directory holds goletter.toml, templates/ and campaigns/, and is meant
// to be kept in version control. Device-local state lives in .goletter/.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		if os.Args[1] == "-h" || os.Args[1] == "--help" {
			fmt.Println("usage: goletter [dir]")
			return
		}
		dir = os.Args[1]
	}
	dir, err := filepath.Abs(dir)
	if err == nil {
		err = run(dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "goletter:", err)
		os.Exit(1)
	}
}
