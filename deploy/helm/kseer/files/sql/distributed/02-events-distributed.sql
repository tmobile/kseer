/* 2) Distributed table — logical router + query fan-out across shards */
CREATE TABLE IF NOT EXISTS ${database}.kubernetes_events
ON CLUSTER ${cluster}
AS ${database}.kubernetes_events_local
ENGINE = Distributed(
    ${cluster},
    ${database},
    kubernetes_events_local,
    sipHash64(cluster_name, uid)
);
