// Command import loads BTN's legacy Excel spreadsheets (block reports, petty cash, wages) into the ledgers.
//
//	make import ARGS=--dry-run     # parse, validate and print the full report; writes nothing
//	make import                    # import data/samples/*.xlsx (refuses if imported data exists)
//	make import ARGS=--replace     # delete ONLY previously imported records, then import again
//
// options: --dir=<folder> --month=YYYY-MM (override the month inferred from file names) --report=<file>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"

	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/importer"
)

func main() {
	var o importer.Options
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.BoolVar(&o.DryRun, "dry-run", false, "parse, validate and print the full report; write nothing")
	fs.BoolVar(&o.Replace, "replace", false, "delete ONLY previously imported records, then import again")
	fs.StringVar(&o.Dir, "dir", "", "folder with the .xlsx files (default: data/samples)")
	fs.StringVar(&o.Month, "month", "", "YYYY-MM, overrides the month inferred from the file names")
	fs.StringVar(&o.Report, "report", "", "also save the printed report to this file")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: import [--dry-run] [--replace] [--dir=<folder>] [--month=YYYY-MM] [--report=<file>]")
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "Unknown or malformed option(s): %v — see --help\n", fs.Args())
		os.Exit(2)
	}
	if o.Month != "" && !regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`).MatchString(o.Month) {
		fmt.Fprintln(os.Stderr, "Unknown or malformed option(s): --month must be YYYY-MM")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	code := importer.Run(ctx, pool, o, os.Stdout)
	pool.Close()
	os.Exit(code)
}
