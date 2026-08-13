/* 1) Local storage table — real data lives here (per shard; replicated within shard) */
CREATE TABLE IF NOT EXISTS ${database}.kubernetes_events_local
ON CLUSTER ${cluster}
(
    timestamp   DateTime64(6) CODEC(Delta, LZ4),
    cluster_name LowCardinality(String) CODEC(ZSTD(1)),
    operation    LowCardinality(String) CODEC(ZSTD(1)),
    kind         LowCardinality(String) CODEC(ZSTD(1)),
    namespace    LowCardinality(String) CODEC(ZSTD(1)),
    uid          String CODEC(ZSTD(1)),
    object       JSON CODEC(ZSTD(3))
)
ENGINE = ReplicatedMergeTree(
    '/clickhouse/tables/{shard}/kubernetes_events_local',
    '{replica}'
)
PARTITION BY toStartOfHour(timestamp)
ORDER BY (kind, cluster_name, namespace, timestamp)
TTL toDateTime(timestamp) + INTERVAL 90 DAY
SETTINGS
    index_granularity_bytes = 10485760,
    min_bytes_for_compact_part = 10485760,
    min_rows_for_compact_part = 100000,
    max_parts_in_total = 10000,
    storage_policy = 'default',
    compress_marks = 1,
    compress_primary_key = 1;
