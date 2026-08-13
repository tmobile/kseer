-- Minimal schema for local kseer-agent debugging.
--
-- Matches the agent's INSERT contract (pkg/outputs/clickhouse/clickhouse.go):
--     INSERT INTO <table> (timestamp, cluster_name, operation, kind, namespace, uid, object)
--
-- The `object` column is a native `JSON` type here, matching production's
-- table_version "object_json". The resume-RV / seed-completion queries the
-- agent runs at startup read `object.metadata.resourceVersion::UInt64`, so the
-- exact behavior you're debugging (kinds skipping the initial LIST because a
-- resume RV + SEEDED sentinel already exist in CH) is reproduced identically
-- to production.
--
-- The JSON type is still gated behind an experimental flag in CH 24.8, so we
-- enable it for this DDL session. The agent enables the same settings on its
-- own connection (allow_experimental_json_type / json_type_escape_dots_in_keys).
-- Keep run-local.sh's CH_TABLE_VERSION (object_json) in sync with this schema.

SET allow_experimental_json_type = 1;

CREATE DATABASE IF NOT EXISTS kseer;

CREATE TABLE IF NOT EXISTS kseer.kubernetes_events
(
    timestamp    DateTime64(3),
    cluster_name String,
    operation    LowCardinality(String),
    kind         LowCardinality(String),
    namespace    String,
    uid          String,
    object       JSON
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (cluster_name, kind, timestamp);
