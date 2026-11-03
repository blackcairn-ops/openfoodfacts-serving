package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
)

const (
	defaultParquetURL = "https://huggingface.co/datasets/openfoodfacts/product-database/resolve/main/food.parquet"
	// JSONL mirror at static.openfoodfacts.org is an alternative if HuggingFace
	// is rate-limiting; switch the URL flag at ingest time.
	defaultCountries = "en:united-kingdom"
	defaultOutDB     = "/data/off-uk.duckdb"
)

// ingestSQL is the DuckDB SQL run inside the ingest transaction.
// %s substitution points:
//  1. output DuckDB path (ATTACH target)
//  2. country filter array literal  e.g. ['en:united-kingdom','en:ireland']
//  3. path to the downloaded parquet file
//
// ingestSQL is a multi-statement DuckDB script; %s substitution points:
//  1. path to the downloaded parquet file
//  2. country filter array literal  e.g. ['en:united-kingdom','en:ireland']
//
// Lambda syntax: uses -> (the original form) which is supported on all DuckDB
// versions the Go driver embeds. Do not add SET lambda_syntax=... — it is not
// supported on all embedded versions.
const ingestSQL = `
-- Country-filtered, column-projected extract.
-- product_name is struct(lang varchar, text varchar)[] — extract English then
-- fall back to main-language entry then first available.
-- nutriments is struct(name varchar, "100g" float, …)[] — flatten to *_100g columns.
CREATE OR REPLACE TABLE products AS
SELECT
  code,
  list_extract(list_filter(product_name, x -> x.lang = 'en'), 1).text
    AS product_name_en,
  coalesce(
    list_extract(list_filter(product_name, x -> x.lang = 'en'), 1).text,
    list_extract(list_filter(product_name, x -> x.lang = 'main'), 1).text,
    list_extract(product_name, 1).text
  ) AS product_name,
  brands,
  serving_size,
  TRY_CAST(serving_quantity AS DOUBLE) AS serving_quantity,
  allergens_tags,
  traces_tags,
  list_extract(list_filter(nutriments, x -> x.name = 'energy-kcal'),1)."100g"         AS "energy-kcal_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'proteins'),1)."100g"            AS "proteins_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'carbohydrates'),1)."100g"       AS "carbohydrates_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'fat'),1)."100g"                 AS "fat_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'saturated-fat'),1)."100g"       AS "saturated-fat_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'sodium'),1)."100g"              AS "sodium_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'fiber'),1)."100g"               AS "fiber_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'sugars'),1)."100g"              AS "sugars_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'polyunsaturated-fat'),1)."100g" AS "polyunsaturated-fat_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'monounsaturated-fat'),1)."100g" AS "monounsaturated-fat_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'trans-fat'),1)."100g"           AS "trans-fat_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'cholesterol'),1)."100g"         AS "cholesterol_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'potassium'),1)."100g"           AS "potassium_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'vitamin-a'),1)."100g"           AS "vitamin-a_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'vitamin-c'),1)."100g"           AS "vitamin-c_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'calcium'),1)."100g"             AS "calcium_100g",
  list_extract(list_filter(nutriments, x -> x.name = 'iron'),1)."100g"               AS "iron_100g"
FROM read_parquet('%s')
WHERE list_has_any(countries_tags, %s)
  AND code IS NOT NULL;

CREATE INDEX idx_code ON products (code);
SELECT count(*) AS ingested_rows FROM products;
`

func buildCountryArray(csv string) string {
	tags := strings.Split(csv, ",")
	quoted := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			quoted = append(quoted, "'"+strings.ReplaceAll(t, "'", "''")+"'")
		}
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func downloadParquet(url, dest string) error {
	slog.Info("downloading parquet", "url", url, "dest", dest)
	resp, err := http.Get(url) //nolint:noctx // ingest is a one-shot CLI; context adds no value here
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	slog.Info("download complete", "bytes", n, "dest", dest)
	return nil
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	parquetURL := fs.String("parquet-url", envOr("OFF_PARQUET_URL", defaultParquetURL),
		"URL of the OFF Parquet export")
	countries := fs.String("countries", envOr("OFF_COUNTRIES", defaultCountries),
		"comma-separated OFF country tags to include, e.g. en:united-kingdom,en:ireland")
	outDB := fs.String("out", envOr("OFF_DB_PATH", defaultOutDB),
		"output DuckDB file path")
	keepParquet := fs.Bool("keep-parquet", false,
		"keep the downloaded parquet file after ingest (default: delete it)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Download the parquet next to the output DB so it's on the same filesystem.
	parquetPath := *outDB + ".parquet.tmp"

	slog.Info("starting ingest",
		"countries", *countries,
		"parquet_url", *parquetURL,
		"out_db", *outDB,
	)

	if err := downloadParquet(*parquetURL, parquetPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// Remove any existing output DB so the CREATE OR REPLACE TABLE starts fresh.
	_ = os.Remove(*outDB)

	db, err := sql.Open("duckdb", *outDB)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	// Ensure all DuckDB settings and ingestion use the same connection.
	db.SetMaxOpenConns(1)

	// Keep bulk ingestion from monopolising the host's resources.
	if _, err := db.Exec("SET memory_limit = '2GB'"); err != nil {
		return fmt.Errorf("set DuckDB memory limit: %w", err)
	}
	if _, err := db.Exec("SET threads = 2"); err != nil {
		return fmt.Errorf("set DuckDB threads: %w", err)
	}
	if _, err := db.Exec("SET temp_directory = ?", *outDB+".tmp"); err != nil {
		return fmt.Errorf("set DuckDB temp directory: %w", err)
	}

	countryArr := buildCountryArray(*countries)
	query := fmt.Sprintf(ingestSQL, parquetPath, countryArr)

	slog.Info("running ingest SQL", "countries", *countries)
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("ingest SQL: %w", err)
	}
	defer rows.Close()

	// Print the row count from the final SELECT.
	if rows.Next() {
		var n int64
		if err := rows.Scan(&n); err == nil {
			slog.Info("ingest complete", "rows", n, "out_db", *outDB)
		}
	}

	if !*keepParquet {
		if err := os.Remove(parquetPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("could not remove parquet", "path", parquetPath, "err", err)
		}
	}
	return nil
}
