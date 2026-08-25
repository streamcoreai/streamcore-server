package rag

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/streamcoreai/streamcore-server/internal/config"
)

// These run against a live pgvector database, because what they check is
// whether the store contract in docs/agent-runtime.md survives contact with
// Postgres — the shape of atttypmod for a vector column, and whether the DDL
// we publish is the DDL the startup check accepts. Without one they skip:
//
//	docker run -d -e POSTGRES_PASSWORD=test -e POSTGRES_DB=ragtest \
//	    -p 55433:5432 pgvector/pgvector:pg16
//	STREAMCORE_TEST_PG=postgres://postgres:test@localhost:55433/ragtest go test ./internal/rag/
func testPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	connString := os.Getenv("STREAMCORE_TEST_PG")
	if connString == "" {
		t.Skip("set STREAMCORE_TEST_PG to a pgvector database to run this")
	}

	pool, err := pgxpool.New(context.Background(), connString)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, connString
}

// documentedDDL is the SQL from docs/agent-runtime.md, kept here so a change to
// one without the other fails a test rather than a deployment.
func documentedDDL(table string, dims int) string {
	return strings.NewReplacer("$TABLE", table, "$DIMS", itoa(dims)).Replace(`
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE $TABLE (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    embedding vector($DIMS) NOT NULL,
    embedding_model TEXT NOT NULL,
    source TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS $TABLE_embedding_model_idx ON $TABLE (embedding_model);`)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func createTable(t *testing.T, pool *pgxpool.Pool, ddl, table string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatalf("create %s: %v", table, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table)
	})
}

func insertRow(t *testing.T, pool *pgxpool.Pool, table string, dims int, model string) {
	t.Helper()
	vec := make([]float32, dims)
	vec[0] = 0.5
	_, err := pool.Exec(context.Background(),
		"INSERT INTO "+table+" (content, embedding, embedding_model, source) VALUES ($1, $2::vector, $3, $4)",
		"the workshop closes at 3pm", formatVector(vec), model, "hours.md")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestVerifyStoreAcceptsTheDocumentedSchema(t *testing.T) {
	pool, _ := testPool(t)
	const table = "rag_documented_docs"
	createTable(t, pool, documentedDDL(table, 1536), table)

	if err := verifyStore(pool, table, "text-embedding-3-small"); err != nil {
		t.Fatalf("empty documented table rejected: %v", err)
	}

	insertRow(t, pool, table, 1536, "text-embedding-3-small")
	if err := verifyStore(pool, table, "text-embedding-3-small"); err != nil {
		t.Fatalf("populated documented table rejected: %v", err)
	}

	// The default model has to be read as text-embedding-3-small, or every
	// config that omits embedding_model would fail against its own store.
	if err := verifyStore(pool, table, ""); err != nil {
		t.Fatalf("empty model config rejected: %v", err)
	}
}

func TestVerifyStoreRejectsAMismatchedStore(t *testing.T) {
	pool, _ := testPool(t)
	const table = "rag_mismatch_docs"
	createTable(t, pool, documentedDDL(table, 1536), table)
	insertRow(t, pool, table, 1536, "text-embedding-ada-002")

	// Same width, so nothing but the recorded model can catch it.
	err := verifyStore(pool, table, "text-embedding-3-small")
	if err == nil {
		t.Fatal("a store built with ada-002 was accepted for 3-small")
	}
	if !strings.Contains(err.Error(), "text-embedding-ada-002") {
		t.Errorf("error does not name the model in the store: %v", err)
	}

	// Different width, caught by the column definition.
	if err := verifyStore(pool, table, "text-embedding-3-large"); err == nil {
		t.Error("a vector(1536) store was accepted for a 3072-wide model")
	}
}

func TestVerifyStoreOnLegacyAndOddTables(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()

	t.Run("missing table", func(t *testing.T) {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS rag_absent_docs")
		if err := verifyStore(pool, "rag_absent_docs", "text-embedding-3-small"); err == nil {
			t.Error("a table that does not exist verified clean")
		}
	})

	t.Run("no embedding_model column", func(t *testing.T) {
		const table = "rag_legacy_docs"
		createTable(t, pool, `CREATE TABLE `+table+` (
			id SERIAL PRIMARY KEY,
			content TEXT NOT NULL,
			embedding vector(1536),
			source TEXT
		)`, table)

		err := verifyStore(pool, table, "text-embedding-3-small")
		if err == nil {
			t.Fatal("an unmigrated table verified clean")
		}
		if !strings.Contains(err.Error(), "ALTER TABLE") {
			t.Errorf("error carries no migration: %v", err)
		}
	})

	t.Run("bare vector column takes any width", func(t *testing.T) {
		const table = "rag_bare_vector_docs"
		createTable(t, pool, `CREATE TABLE `+table+` (
			id SERIAL PRIMARY KEY,
			content TEXT NOT NULL,
			embedding vector,
			embedding_model TEXT NOT NULL
		)`, table)

		// atttypmod is -1 here, so there is no declared width to disagree with.
		if err := verifyStore(pool, table, "text-embedding-3-large"); err != nil {
			t.Errorf("undeclared width rejected: %v", err)
		}
	})

	t.Run("null model reads as unmigrated rows", func(t *testing.T) {
		const table = "rag_null_model_docs"
		createTable(t, pool, `CREATE TABLE `+table+` (
			id SERIAL PRIMARY KEY,
			content TEXT NOT NULL,
			embedding vector(1536),
			embedding_model TEXT
		)`, table)

		vec := make([]float32, 1536)
		if _, err := pool.Exec(ctx,
			"INSERT INTO "+table+" (content, embedding) VALUES ($1, $2::vector)", "chunk", formatVector(vec)); err != nil {
			t.Fatalf("insert: %v", err)
		}

		err := verifyStore(pool, table, "text-embedding-3-small")
		if err == nil || !strings.Contains(err.Error(), "(null)") {
			t.Errorf("null embedding_model = %v, want a mismatch naming (null)", err)
		}
	})
}

func TestNewPgvectorClientRefusesABadStore(t *testing.T) {
	_, connString := testPool(t)
	pool, _ := testPool(t)
	const table = "rag_client_docs"
	createTable(t, pool, documentedDDL(table, 1536), table)
	insertRow(t, pool, table, 1536, "text-embedding-ada-002")

	cfg := &config.Config{}
	cfg.Pgvector.ConnectionString = connString
	cfg.Pgvector.Table = table
	cfg.RAG.EmbeddingModel = "text-embedding-3-small"

	// main.go turns this error into log.Fatalf, so a mismatch here is the
	// server declining to boot.
	if _, err := NewPgvectorClient(cfg); err == nil {
		t.Error("client constructed against a store built with another model")
	}

	cfg.RAG.EmbeddingModel = "text-embedding-ada-002"
	client, err := NewPgvectorClient(cfg)
	if err != nil {
		t.Fatalf("client rejected its own store: %v", err)
	}
	_ = client
}
