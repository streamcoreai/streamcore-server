package rag

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const defaultEmbeddingModel = "text-embedding-3-small"

// Output width of every embedding model we know about. Width catches half the
// contract: Postgres rejects a 3072-wide vector against a vector(1536) column
// on its own. The other half is the same-width case — ada-002 and 3-small are
// both 1536 and produce no error at all, only bad retrieval — which is why
// every row also carries the model that wrote it.
var modelDimensions = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

// dimensionsFor reports the vector width for model. ok is false for models
// released after this table was written; callers skip the width check instead
// of refusing to start on a model they simply haven't heard of.
func dimensionsFor(model string) (dims int, ok bool) {
	if model == "" {
		model = defaultEmbeddingModel
	}
	dims, ok = modelDimensions[model]
	return dims, ok
}

func normalizeModel(model string) string {
	if model == "" {
		return defaultEmbeddingModel
	}
	return model
}

// dimensionMismatch names both ways out, because which one is right depends on
// whether the config or the store is the thing that changed.
func dimensionMismatch(table, model string, want, got int) error {
	model = normalizeModel(model)
	return fmt.Errorf(
		"table %q holds vector(%d) but embedding_model %q produces %d dimensions: "+
			"re-ingest with streamcore-cli against a vector(%d) column, or set "+
			"rag.embedding_model back to the model the store was built with",
		table, got, model, want, want)
}

// modelMismatch is the failure the width check cannot see: same-size vectors
// from a different model, which retrieve nonsense without erroring anywhere.
func modelMismatch(table, want, got string) error {
	if got == "" {
		got = "(null)"
	}
	return fmt.Errorf(
		"table %q holds rows embedded with %q but rag.embedding_model is %q: "+
			"vectors from different models are not comparable, so re-ingest with "+
			"streamcore-cli or set rag.embedding_model back to %[2]q",
		table, got, normalizeModel(want))
}

// missingModelColumn carries the migration inline. Anyone hitting this ingested
// before the column existed, and the value to backfill is something only they
// know, so the UPDATE is left with a placeholder rather than a guess.
func missingModelColumn(table string) error {
	return fmt.Errorf(
		"table %q has no embedding_model column, so the model its vectors were "+
			"written with cannot be checked. Migrate it:\n"+
			"    ALTER TABLE %[1]s ADD COLUMN embedding_model TEXT;\n"+
			"    UPDATE %[1]s SET embedding_model = '<the model you ingested with>';\n"+
			"    ALTER TABLE %[1]s ALTER COLUMN embedding_model SET NOT NULL;\n"+
			"    CREATE INDEX IF NOT EXISTS %[1]s_embedding_model_idx ON %[1]s (embedding_model);",
		table)
}

// vectorWidth counts the dimensions in an embedding column value as PostgREST
// returns it. pgvector has no JSON mapping there, so a row arrives as the
// quoted text form "[0.1,0.2]" rather than a JSON array — accept both, since
// the day PostgREST learns the type is the day this silently stops checking.
func vectorWidth(raw json.RawMessage) (int, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		if s == "" {
			return 0, false
		}
		parts := strings.Split(s, ",")
		for _, p := range parts {
			if _, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err != nil {
				return 0, false
			}
		}
		return len(parts), true
	}

	var vec []float64
	if err := json.Unmarshal(raw, &vec); err != nil || len(vec) == 0 {
		return 0, false
	}
	return len(vec), true
}
