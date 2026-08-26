// Command softmap extracts human-readable flow graphs from Go codebases.
package main

import (
	"flag"
	"fmt"
	"os"
)

// toolName is the single place the working codename appears; a rename is a
// one-line change here (plus the Makefile NAME variable for the binary path).
const toolName = "softmap"

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  %[1]s scan <path>                      list discovered entrypoints
  %[1]s scan <path> --entrypoint <id>    emit flow graph JSON for one entrypoint
  %[1]s scan <path> --all -o <dir>       one JSON file per entrypoint
  %[1]s rules --defaults                 print the embedded default filter rules

Run "%[1]s <command> -h" for command flags.
`, toolName)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "scan":
		err = runScan(os.Args[2:])
	case "rules":
		err = runRules(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "%s: unknown command %q\n", toolName, os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", toolName, err)
		os.Exit(1)
	}
}
