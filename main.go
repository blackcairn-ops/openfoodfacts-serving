// foodfacts is a self-hosted Open Food Facts API shim backed by a DuckDB
// file containing a country-filtered subset of the OFF product database.
//
// Commands:
//
//	foodfacts serve   — HTTP server on :8080 implementing the two OFF endpoints
//	                    SparkyFitness calls (text search + barcode lookup).
//	foodfacts ingest  — Download the OFF Parquet export, filter to the
//	                    configured countries, and write a compact DuckDB file.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: foodfacts <serve|ingest|validate>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		if err := cmdServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "ingest":
		if err := cmdIngest(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ingest:", err)
			os.Exit(1)
		}
	case "validate":
		if err := cmdValidate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "validate:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q — use serve or ingest\n", os.Args[1])
		os.Exit(1)
	}
}
