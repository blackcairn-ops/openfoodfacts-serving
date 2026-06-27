# openfoodfacts-serving

A small Go service that serves the two Open Food Facts API endpoints
SparkyFitness calls (`/cgi/search.pl` and `/api/v2/product/{barcode}.json`)
from a compact, country-filtered DuckDB file extracted from the OFF Parquet
export. The same binary has an `ingest` subcommand that builds the DB.

## Why self-host

The public OFF API is slow and rate-limited. A country-filtered extract is
tiny: **187,900 UK products → 15 MB DuckDB file** (vs 7.6 GB full Parquet).
Search latency drops from ~1–3 s on the public API to <5 ms on a local ILIKE
scan.

## Data source

- **Parquet export**: `https://huggingface.co/datasets/openfoodfacts/product-database/resolve/main/food.parquet` (7.6 GB)
- **JSONL mirror** (if HuggingFace rate-limits):
  `https://static.openfoodfacts.org/data/openfoodfacts-products.jsonl.gz`

OFF updates the export daily; refreshing monthly or quarterly is plenty.

> **Important**: Do NOT stream-filter the Parquet over HTTP with DuckDB's
> httpfs. HuggingFace 429s after ~130 s of range requests. Always download
> first, then ingest locally.

## Endpoints

| Endpoint                            | Used by                              |
|-------------------------------------|--------------------------------------|
| `GET /cgi/search.pl`                | Text search                          |
| `GET /api/v2/product/{barcode}.json`| Barcode point-lookup                 |
| `POST /cgi/session.pl`              | OFF session login (stub: 200 OK)     |
| `GET /api/health`                   | Liveness probe                       |

Point SparkyFitness at this service via the
[`OPENFOODFACTS_BASE_URL`](https://github.com/CodeWithCJ/SparkyFitness/issues/1657)
env var (e.g. `http://openfoodfacts-serving:8080`).

## Running ingest

The `ingest` subcommand downloads the Parquet to a temp file adjacent to the
output DB, runs the DuckDB extract, then deletes the temp file. The DB file
must be on a disk-backed path (not tmpfs — most tmpfs mounts are too small
for the 7.6 GB download).

```sh
# First-time or refresh:
go run . ingest

# Or with explicit flags:
go run . ingest \
  --countries en:united-kingdom \
  --out ./off-uk.duckdb

# Keep the downloaded parquet for debugging:
go run . ingest --keep-parquet
```

### Environment variables (all optional)

| Variable          | Default                                                          | Description                      |
|-------------------|------------------------------------------------------------------|----------------------------------|
| `OFF_COUNTRIES`   | `en:united-kingdom`                                              | Comma-separated OFF country tags |
| `OFF_DB_PATH`     | `/data/off-uk.duckdb`                                            | Output DuckDB file path          |
| `OFF_PARQUET_URL` | `https://huggingface.co/datasets/openfoodfacts/product-database/resolve/main/food.parquet` | Source Parquet URL               |
| `OFF_ADDR`        | `:8080`                                                          | Serve listen address             |

## Adding a country

Country tags use the format `en:<slug>`. Examples:

| Country        | Tag                 |
|----------------|---------------------|
| United Kingdom | `en:united-kingdom` |
| Ireland        | `en:ireland`        |
| France         | `en:france`         |
| Germany        | `en:germany`        |
| Spain          | `en:spain`          |
| United States  | `en:united-states`  |

Export `OFF_COUNTRIES` before running `ingest`:

```sh
export OFF_COUNTRIES="en:united-kingdom,en:ireland"
go run . ingest
```

Expect ~5–15k additional rows per country; the file stays well under 100 MB
for most multi-country combinations.

### Discover country tags

To find the exact tag for a country (and its product count), run this
against a downloaded parquet file:

```sh
duckdb food.parquet -c "
SELECT unnest(countries_tags) AS tag, count(*) AS products
FROM read_parquet('food.parquet')
GROUP BY tag
HAVING products > 1000
ORDER BY products DESC
LIMIT 30;
"
```

## Docker

A multi-stage Dockerfile is included (CGO is required for the
`duckdb-go/v2` driver):

```sh
docker build -t openfoodfacts-serving .
docker run --rm -v "$PWD/data:/data" openfoodfacts-serving ingest
docker run --rm -v "$PWD/data:/data" -p 8080:8080 openfoodfacts-serving serve
```

## Validation queries

After ingest, verify the DB with `duckdb`:

```sh
duckdb ./off-uk.duckdb -c "
SELECT count(*) total,
       count(product_name) have_name,
       count(\"energy-kcal_100g\") have_kcal
FROM products;
"

# Barcode point-lookup (Heinz Baked Beans)
duckdb ./off-uk.duckdb -c "
SELECT code, product_name, brands, \"energy-kcal_100g\" kcal
FROM products WHERE code = '5000157024909';
"

# Name search
duckdb ./off-uk.duckdb -c "
SELECT code, product_name, brands FROM products
WHERE product_name ILIKE '%hovis%' LIMIT 5;
"
```

Expected: ~187,900 UK rows, Heinz Baked Beans → 71 kcal/100g, Hovis breads
present.

## License

MIT
