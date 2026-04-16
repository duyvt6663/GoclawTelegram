CREATE OR REPLACE FUNCTION immutable_array_to_string(arr text[], sep text)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
$$SELECT array_to_string(arr, sep)$$;

ALTER TABLE episodic_summaries
    ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (
        to_tsvector(
            'simple'::regconfig,
            COALESCE(summary, '') || ' ' || COALESCE(immutable_array_to_string(key_topics, ' '), '')
        )
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_episodic_search_vector
    ON episodic_summaries USING GIN (search_vector);

CREATE INDEX IF NOT EXISTS idx_episodic_embedding_hnsw
    ON episodic_summaries USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE embedding IS NOT NULL;
