package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/softmapio/softmap/internal/filter"
)

func runRules(args []string) error {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	defaults := fs.Bool("defaults", false, "print the embedded default rule file")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s rules --defaults\n\nFlags:\n", toolName)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*defaults {
		fs.Usage()
		return fmt.Errorf("rules: only --defaults is supported in this milestone")
	}
	_, err := os.Stdout.Write(filter.DefaultYAML())
	return err
}
