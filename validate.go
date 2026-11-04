package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
)

func productCount(path string) (int64, error) {
	if path == "" {
		return 0, errors.New("database path is empty")
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return 0, fmt.Errorf("database is missing data: %s", path)
	}

	db, err := sql.Open("duckdb", path+"?access_mode=read_only")
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()

	var count int64
	if err := db.QueryRow("SELECT count(*) FROM products").Scan(&count); err != nil {
		return 0, fmt.Errorf("count products in %s: %w", path, err)
	}

	return count, nil
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	current := fs.String("current", "", "current DuckDB database")
	staging := fs.String("staging", "", "staged DuckDB database")
	minRatio := fs.Float64("min-ratio", 0.90, "minimum staged/current product count ratio")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *current == "" || *staging == "" {
		return errors.New("--current and --staging are required")
	}
	if *current == *staging {
		return errors.New("current and staging paths must differ")
	}
	if math.IsNaN(*minRatio) || math.IsInf(*minRatio, 0) ||
		*minRatio <= 0 || *minRatio > 1 {
		return errors.New("--min-ratio must be greater than 0 and at most 1")
	}

	currentCount, err := productCount(*current)
	if err != nil {
		return err
	}
	stagingCount, err := productCount(*staging)
	if err != nil {
		return err
	}
	if currentCount <= 0 || stagingCount <= 0 {
		return fmt.Errorf("invalid product counts: current=%d staging=%d",
			currentCount, stagingCount)
	}

	ratio := float64(stagingCount) / float64(currentCount)
	fmt.Printf("Current: %d products\nStaging: %d products\nRatio: %.2f%%\n",
		currentCount, stagingCount, ratio*100)

	if ratio < *minRatio {
		return fmt.Errorf("staging database below %.0f%% minimum", *minRatio*100)
	}

	fmt.Println("Validation passed")
	return nil
}
