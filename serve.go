package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
)

// Product JSON shape must exactly match what SparkyFitness's
// mapOpenFoodFactsProduct (openFoodFactsService.ts) reads from each product.
type nutriments struct {
	EnergyKcal100g    *float64 `json:"energy-kcal_100g,omitempty"`
	Proteins100g      *float64 `json:"proteins_100g,omitempty"`
	Carbohydrates100g *float64 `json:"carbohydrates_100g,omitempty"`
	Fat100g           *float64 `json:"fat_100g,omitempty"`
	SaturatedFat100g  *float64 `json:"saturated-fat_100g,omitempty"`
	Sodium100g        *float64 `json:"sodium_100g,omitempty"`
	Fiber100g         *float64 `json:"fiber_100g,omitempty"`
	Sugars100g        *float64 `json:"sugars_100g,omitempty"`
	PolyunsatFat100g  *float64 `json:"polyunsaturated-fat_100g,omitempty"`
	MonounsatFat100g  *float64 `json:"monounsaturated-fat_100g,omitempty"`
	TransFat100g      *float64 `json:"trans-fat_100g,omitempty"`
	Cholesterol100g   *float64 `json:"cholesterol_100g,omitempty"`
	Potassium100g     *float64 `json:"potassium_100g,omitempty"`
	VitaminA100g      *float64 `json:"vitamin-a_100g,omitempty"`
	VitaminC100g      *float64 `json:"vitamin-c_100g,omitempty"`
	Calcium100g       *float64 `json:"calcium_100g,omitempty"`
	Iron100g          *float64 `json:"iron_100g,omitempty"`
}

type product struct {
	Code            string     `json:"code"`
	ProductName     string     `json:"product_name"`
	ProductNameEn   string     `json:"product_name_en"`
	Brands          string     `json:"brands"`
	ServingSize     string     `json:"serving_size"`
	ServingQuantity *float64   `json:"serving_quantity"`
	Nutriments      nutriments `json:"nutriments"`
	AllergensTag    []string   `json:"allergens_tags"`
	TracesTags      []string   `json:"traces_tags"`
}

type searchResponse struct {
	Products []product `json:"products"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
	Count    int       `json:"count"`
}

type barcodeResponse struct {
	Status  int     `json:"status"`
	Code    string  `json:"code"`
	Product product `json:"product"`
}

const selectCols = `
	code,
	coalesce(product_name_en, product_name, '') AS product_name,
	coalesce(product_name_en, product_name, '') AS product_name_en,
	coalesce(brands, '')                         AS brands,
	coalesce(serving_size, '')                   AS serving_size,
	serving_quantity,
	"energy-kcal_100g", "proteins_100g", "carbohydrates_100g", "fat_100g",
	"saturated-fat_100g", "sodium_100g", "fiber_100g", "sugars_100g",
	"polyunsaturated-fat_100g", "monounsaturated-fat_100g", "trans-fat_100g",
	"cholesterol_100g", "potassium_100g", "vitamin-a_100g", "vitamin-c_100g",
	"calcium_100g", "iron_100g",
	to_json(allergens_tags)::varchar AS allergens_json,
	to_json(traces_tags)::varchar    AS traces_json
`

func f64ptr(n sql.NullFloat64) *float64 {
	if n.Valid {
		v := n.Float64
		return &v
	}
	return nil
}

func scanProduct(rows *sql.Rows) (product, error) {
	var (
		p                                                       product
		servQty                                                 sql.NullFloat64
		kcal, prot, carbs, fat, satFat, sodium, fiber, sugars   sql.NullFloat64
		poly, mono, transFat, chol, pot, vitA, vitC, calc, iron sql.NullFloat64
		allergens, traces                                       string
	)
	err := rows.Scan(
		&p.Code, &p.ProductName, &p.ProductNameEn, &p.Brands,
		&p.ServingSize, &servQty,
		&kcal, &prot, &carbs, &fat, &satFat, &sodium, &fiber, &sugars,
		&poly, &mono, &transFat, &chol, &pot, &vitA, &vitC, &calc, &iron,
		&allergens, &traces,
	)
	if err != nil {
		return p, err
	}
	p.ServingQuantity = f64ptr(servQty)
	p.Nutriments = nutriments{
		EnergyKcal100g:    f64ptr(kcal),
		Proteins100g:      f64ptr(prot),
		Carbohydrates100g: f64ptr(carbs),
		Fat100g:           f64ptr(fat),
		SaturatedFat100g:  f64ptr(satFat),
		Sodium100g:        f64ptr(sodium),
		Fiber100g:         f64ptr(fiber),
		Sugars100g:        f64ptr(sugars),
		PolyunsatFat100g:  f64ptr(poly),
		MonounsatFat100g:  f64ptr(mono),
		TransFat100g:      f64ptr(transFat),
		Cholesterol100g:   f64ptr(chol),
		Potassium100g:     f64ptr(pot),
		VitaminA100g:      f64ptr(vitA),
		VitaminC100g:      f64ptr(vitC),
		Calcium100g:       f64ptr(calc),
		Iron100g:          f64ptr(iron),
	}
	if err := json.Unmarshal([]byte(allergens), &p.AllergensTag); err != nil || p.AllergensTag == nil {
		p.AllergensTag = []string{}
	}
	if err := json.Unmarshal([]byte(traces), &p.TracesTags); err != nil || p.TracesTags == nil {
		p.TracesTags = []string{}
	}
	return p, nil
}

// buildSearchWhere returns a WHERE clause and argument slice requiring every
// whitespace-separated word in the query to appear in product_name,
// product_name_en, or brands. Splitting on words lets "hovis wholemeal" match
// products whose name contains both words without a strict phrase order.
func buildSearchWhere(query string) (string, []any) {
	words := strings.Fields(query)
	if len(words) == 0 {
		return "1=0", nil
	}
	var clauses []string
	var args []any
	for _, w := range words {
		pat := "%" + w + "%"
		clauses = append(clauses, "(product_name ILIKE ? OR product_name_en ILIKE ? OR brands ILIKE ?)")
		args = append(args, pat, pat, pat)
	}
	return strings.Join(clauses, " AND "), args
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("OFF_DB_PATH", "/data/off-uk.duckdb"), "path to DuckDB file")
	addr := fs.String("addr", envOr("OFF_ADDR", ":8080"), "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	db, err := sql.Open("duckdb", *dbPath+"?access_mode=read_only")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping db %q: %w", *dbPath, err)
	}
	slog.Info("foodfacts serving", "db", *dbPath, "addr", *addr)

	mux := http.NewServeMux()

	// Health probe used by the compose healthcheck.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Session stub — SparkyFitness calls this when the user has OFF account
	// credentials configured; our self-hosted instance has no accounts.
	mux.HandleFunc("/cgi/session.pl", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Text search: GET /cgi/search.pl?search_terms=TEXT&page=N
	mux.HandleFunc("GET /cgi/search.pl", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := q.Get("search_terms")
		if query == "" {
			writeJSON(w, http.StatusOK, searchResponse{Products: []product{}, Page: 1, PageSize: 20, Count: 0})
			return
		}
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		pageSize, _ := strconv.Atoi(q.Get("page_size"))
		if pageSize < 1 {
			pageSize = 20
		}
		if pageSize > 100 {
			pageSize = 100
		}
		offset := (page - 1) * pageSize

		where, args := buildSearchWhere(query)

		var count int
		if err := db.QueryRow("SELECT count(*) FROM products WHERE "+where, args...).Scan(&count); err != nil {
			slog.Error("search count", "err", err, "query", query)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}

		pageArgs := append(args, pageSize, offset) //nolint:gocritic // slice is local
		rows, err := db.Query("SELECT "+selectCols+" FROM products WHERE "+where+
			" ORDER BY product_name LIMIT ? OFFSET ?", pageArgs...)
		if err != nil {
			slog.Error("search select", "err", err, "query", query)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		defer rows.Close()

		products := make([]product, 0, pageSize)
		for rows.Next() {
			p, err := scanProduct(rows)
			if err != nil {
				slog.Error("scan product", "err", err)
				continue
			}
			products = append(products, p)
		}
		writeJSON(w, http.StatusOK, searchResponse{
			Products: products,
			Page:     page,
			PageSize: pageSize,
			Count:    count,
		})
	})

	// Barcode lookup: GET /api/v2/product/{barcode}.json
	// The .json suffix prevents a simple {barcode} wildcard (ServeMux wildcards
	// must be whole path segments), so we register the prefix and strip it manually.
	// Returns 404 on miss; SparkyFitness maps 404 → {status:0, status_verbose:'product not found'}.
	mux.HandleFunc("GET /api/v2/product/", func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/v2/product/<barcode>.json — strip prefix and .json suffix.
		seg := strings.TrimPrefix(r.URL.Path, "/api/v2/product/")
		barcode := strings.TrimSuffix(seg, ".json")
		rows, err := db.Query("SELECT "+selectCols+" FROM products WHERE code = ? LIMIT 1", barcode)
		if err != nil {
			slog.Error("barcode query", "err", err, "barcode", barcode)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		defer rows.Close()

		if !rows.Next() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		p, err := scanProduct(rows)
		if err != nil {
			slog.Error("scan barcode product", "err", err, "barcode", barcode)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "scan error"})
			return
		}
		writeJSON(w, http.StatusOK, barcodeResponse{Status: 1, Code: p.Code, Product: p})
	})

	return http.ListenAndServe(*addr, mux)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
