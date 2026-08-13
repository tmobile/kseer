#!/usr/bin/env bash
#
# ClickHouse table initialization — shell implementation.
#
# Functionally equivalent to ../init-scripts-js/index.js: it renders every
# .sql file for the selected mode (substituting ${database} and ${cluster})
# and executes them in filename order. Use this when you would rather not
# install Node.js; it needs only bash and curl, and talks to ClickHouse's
# HTTP interface.
#
# Two topologies are supported, chosen with --mode:
#   distributed  ReplicatedMergeTree + Distributed table across a cluster
#   single       one plain MergeTree table on a single node
# Either way the agent writes to, and the UI reads from, `kubernetes_events`.
#
# Each .sql file must contain exactly one statement — the HTTP interface
# rejects multi-statement requests.
#
# Usage:
#   ./init.sh [--mode distributed|single] [--env-file PATH] [--sql-dir PATH]
#             [--drop] [--dry-run]
#
# See ./README.md for configuration.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/.env"
DRY_RUN=false
DROP_OVERRIDE=""
MODE_OVERRIDE=""
SQL_DIR=""

usage() {
    cat <<'EOF'
Initialize the KSeer ClickHouse schema.

Usage: ./init.sh [options]

Options:
  --mode MODE       distributed (default) or single; see below
  --env-file PATH   Environment file to load (default: ./.env next to this script)
  --sql-dir PATH    Apply this directory instead of the one implied by --mode
  --drop            Drop existing tables first; same as DROP_EXISTING=true
  --dry-run         Print the rendered SQL without executing anything
  -h, --help        Show this help

Modes:
  distributed  ReplicatedMergeTree storage per shard plus a Distributed table,
               created ON CLUSTER. Needs a ClickHouse cluster with Keeper.
  single       One plain MergeTree table on a single node. No cluster, no
               replication, and CLICKHOUSE_CLUSTER is ignored.

  Both modes expose the same table, kubernetes_events, with the same columns,
  so the agent and UI are configured identically either way.

Required environment:
  CLICKHOUSE_URL        e.g. https://clickhouse.example.com:8443 (HTTP interface)
  CLICKHOUSE_DATABASE   target database; must already exist

Optional environment:
  CLICKHOUSE_MODE       distributed | single (default: distributed)
  CLICKHOUSE_USER       default: empty (ClickHouse decides)
  CLICKHOUSE_PASS       default: empty
  CLICKHOUSE_CLUSTER    default: default (distributed mode only)
  DROP_EXISTING         true to drop tables before creating (default: false)
  CLICKHOUSE_TLS_SKIP_VERIFY  true to skip certificate verification (default: false)
EOF
}

log()      { printf '%s\n' "$*"; }
log_ok()   { printf '✅ %s\n' "$*"; }
log_warn() { printf '⚠️  %s\n' "$*" >&2; }
log_err()  { printf '❌ %s\n' "$*" >&2; }

is_true() {
    case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
        1|true|yes) return 0 ;;
        *) return 1 ;;
    esac
}

while [ $# -gt 0 ]; do
    case "$1" in
        --mode)     MODE_OVERRIDE="${2:?--mode needs a value}"; shift 2 ;;
        --env-file) ENV_FILE="${2:?--env-file needs a path}"; shift 2 ;;
        --sql-dir)  SQL_DIR="${2:?--sql-dir needs a path}"; shift 2 ;;
        --drop)     DROP_OVERRIDE=true; shift ;;
        --dry-run)  DRY_RUN=true; shift ;;
        -h|--help)  usage; exit 0 ;;
        *) log_err "Unknown option: $1"; usage >&2; exit 2 ;;
    esac
done

if [ -f "$ENV_FILE" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
fi

CLICKHOUSE_URL="${CLICKHOUSE_URL:-}"
CLICKHOUSE_DATABASE="${CLICKHOUSE_DATABASE:-}"
CLICKHOUSE_USER="${CLICKHOUSE_USER:-}"
CLICKHOUSE_PASS="${CLICKHOUSE_PASS:-}"
CLICKHOUSE_CLUSTER="${CLICKHOUSE_CLUSTER:-default}"
DROP_EXISTING="${DROP_OVERRIDE:-${DROP_EXISTING:-false}}"
MODE="${MODE_OVERRIDE:-${CLICKHOUSE_MODE:-distributed}}"

case "$MODE" in
    distributed)
        # The distributed table depends on the local table, so it drops first.
        DROP_ORDER="kubernetes_events kubernetes_events_local"
        ON_CLUSTER=" ON CLUSTER $CLICKHOUSE_CLUSTER"
        ;;
    single)
        DROP_ORDER="kubernetes_events"
        ON_CLUSTER=""
        ;;
    *)
        log_err "Unknown mode: $MODE (expected 'distributed' or 'single')"
        exit 2
        ;;
esac

if [ -z "$CLICKHOUSE_URL" ] || [ -z "$CLICKHOUSE_DATABASE" ]; then
    log_err "CLICKHOUSE_URL and CLICKHOUSE_DATABASE are required."
    log_err "Copy .env.example to .env and fill it in, or pass --env-file."
    exit 1
fi

# An explicit --sql-dir wins; otherwise the mode picks the directory.
if [ -z "$SQL_DIR" ]; then
    SQL_DIR="$SCRIPT_DIR/../sql/$MODE"
fi

if [ ! -d "$SQL_DIR" ]; then
    log_err "SQL directory not found: $SQL_DIR"
    exit 1
fi
SQL_DIR="$(cd "$SQL_DIR" && pwd)"

command -v curl >/dev/null 2>&1 || { log_err "curl is required but not installed."; exit 1; }

CURL_OPTS=(--silent --show-error --fail-with-body)
if is_true "${CLICKHOUSE_TLS_SKIP_VERIFY:-}"; then
    CURL_OPTS+=(--insecure)
fi
if [ -n "$CLICKHOUSE_USER" ]; then
    CURL_OPTS+=(--user "$CLICKHOUSE_USER:$CLICKHOUSE_PASS")
fi

# --fail-with-body arrived in curl 7.76; fall back to --fail on older builds so
# the script still reports failures, just without ClickHouse's error text.
if ! curl --help all 2>/dev/null | grep -q -- '--fail-with-body'; then
    CURL_OPTS=("${CURL_OPTS[@]/--fail-with-body/--fail}")
fi

# Send one statement to ClickHouse over the HTTP interface.
ch_exec() {
    printf '%s' "$1" | curl "${CURL_OPTS[@]}" \
        --data-binary @- \
        "${CLICKHOUSE_URL%/}/?allow_experimental_json_type=1"
}

# Substitute the same template variables the Node script supports. Bash's
# pattern replacement is used instead of sed so that database and cluster
# names never get reinterpreted as regex or as a sed delimiter.
render() {
    local sql
    sql="$(cat "$1")"
    sql="${sql//'${database}'/$CLICKHOUSE_DATABASE}"
    sql="${sql//'${cluster}'/$CLICKHOUSE_CLUSTER}"
    printf '%s' "$sql"
}

# Collect .sql files in filename order. Uses a glob rather than `ls` so that
# names with spaces survive, and stays POSIX-ish for bash 3.2 on macOS.
sql_files=()
for f in "$SQL_DIR"/*.sql; do
    [ -e "$f" ] || continue
    sql_files+=("$f")
done

if [ "${#sql_files[@]}" -eq 0 ]; then
    log_err "No .sql files found in $SQL_DIR"
    exit 1
fi

log "========================================"
log "ClickHouse Table Initialization"
log "========================================"
log "Mode:          $MODE"
log "URL:           $CLICKHOUSE_URL"
log "Database:      $CLICKHOUSE_DATABASE"
if [ "$MODE" = distributed ]; then
    log "Cluster:       $CLICKHOUSE_CLUSTER"
fi
log "SQL Directory: $SQL_DIR"
log "Drop existing: $DROP_EXISTING"
log "Dry run:       $DRY_RUN"
log "========================================"
log ""

if [ "$DRY_RUN" = true ]; then
    for f in "${sql_files[@]}"; do
        log "-- ---------- $(basename "$f") ----------"
        render "$f"
        log ""
        log ""
    done
    log_ok "Dry run complete; nothing was executed."
    exit 0
fi

# The script creates tables, not the database. Check connectivity and the
# database separately so a wrong password does not get reported as a missing
# database, and so every CREATE does not fail one by one.
if ! db_count="$(ch_exec "SELECT count() FROM system.databases WHERE name = '$CLICKHOUSE_DATABASE'")"; then
    log_err "Could not query ClickHouse at $CLICKHOUSE_URL"
    log_err "Check the URL, credentials, and TLS settings. Response: ${db_count:-<empty>}"
    exit 1
fi

if [ "$(printf '%s' "$db_count" | tr -d '[:space:]')" = "0" ]; then
    log_err "Database '$CLICKHOUSE_DATABASE' does not exist."
    log_err "Create it first:  CREATE DATABASE $CLICKHOUSE_DATABASE$ON_CLUSTER"
    exit 1
fi

# Catch the common mismatch of running distributed mode against a single node,
# where ON CLUSTER and ReplicatedMergeTree would otherwise fail obscurely.
if [ "$MODE" = distributed ]; then
    if ! cluster_count="$(ch_exec "SELECT count() FROM system.clusters WHERE cluster = '$CLICKHOUSE_CLUSTER'")"; then
        log_warn "Could not verify cluster '$CLICKHOUSE_CLUSTER'; continuing."
    elif [ "$(printf '%s' "$cluster_count" | tr -d '[:space:]')" = "0" ]; then
        log_err "No ClickHouse cluster named '$CLICKHOUSE_CLUSTER' exists."
        log_err "Set CLICKHOUSE_CLUSTER to a cluster from system.clusters, or if this"
        log_err "is a single node, re-run with:  ./init.sh --mode single"
        exit 1
    fi

    # A cluster entry alone is not enough: stock single-node images ship a
    # "default" cluster but no Keeper, and ReplicatedMergeTree needs one.
    # system.zookeeper_connection only exists once Keeper/ZooKeeper is
    # configured, so its presence is the cheapest reliable probe.
    if ! keeper="$(ch_exec "SELECT count() FROM system.tables WHERE database = 'system' AND name = 'zookeeper_connection'")"; then
        log_warn "Could not verify Keeper availability; continuing."
    elif [ "$(printf '%s' "$keeper" | tr -d '[:space:]')" = "0" ]; then
        log_err "This server has no ClickHouse Keeper (or ZooKeeper) configured, which"
        log_err "ReplicatedMergeTree requires. Cluster '$CLICKHOUSE_CLUSTER' exists but"
        log_err "cannot replicate. For a standalone node, re-run with:"
        log_err "  ./init.sh --mode single"
        exit 1
    fi

    # The shipped DDL substitutes {shard} and {replica} into the replica path.
    if missing_macros="$(ch_exec "SELECT arrayStringConcat(arraySort(groupArray(m)), ', ') FROM (SELECT arrayJoin(['shard', 'replica']) AS m) WHERE m NOT IN (SELECT macro FROM system.macros)")"; then
        missing_macros="$(printf '%s' "$missing_macros" | tr -d '\n')"
        if [ -n "$missing_macros" ]; then
            log_warn "Server defines no '$missing_macros' macro(s). The shipped"
            log_warn "distributed DDL references {shard} and {replica}; either define them"
            log_warn "in the server config or edit sql/distributed/01-events-local.sql."
        fi
    fi
fi

# Switching topology over an existing install is silently wrong otherwise: every
# statement is CREATE ... IF NOT EXISTS, so an existing kubernetes_events of the
# other engine survives, and the agent keeps writing to a table that is no longer
# part of the intended layout. Skipped when dropping, since that recreates it.
if ! is_true "$DROP_EXISTING"; then
    if [ "$MODE" = distributed ]; then expected_engine="Distributed"; else expected_engine="MergeTree"; fi
    if existing_engine="$(ch_exec "SELECT engine FROM system.tables WHERE database = '$CLICKHOUSE_DATABASE' AND name = 'kubernetes_events'")"; then
        existing_engine="$(printf '%s' "$existing_engine" | tr -d '[:space:]')"
        if [ -n "$existing_engine" ] && [ "$existing_engine" != "$expected_engine" ]; then
            log_err "$CLICKHOUSE_DATABASE.kubernetes_events already exists with engine"
            log_err "$existing_engine, but $MODE mode expects $expected_engine."
            log_err "CREATE ... IF NOT EXISTS would leave the existing table in place, so the"
            log_err "topology would not actually change. Re-run with --drop to recreate it."
            exit 1
        fi
    fi
fi

if is_true "$DROP_EXISTING"; then
    log "Dropping existing tables..."
    for table in $DROP_ORDER; do
        # SYNC is required, not cosmetic: without it the Atomic database engine
        # defers the drop for database_atomic_delay_before_drop_table_sec, so the
        # ReplicatedMergeTree replica path lingers in Keeper and recreating the
        # table immediately fails with REPLICA_ALREADY_EXISTS.
        if ch_exec "DROP TABLE IF EXISTS $CLICKHOUSE_DATABASE.$table$ON_CLUSTER SYNC" >/dev/null 2>&1; then
            log_ok "Dropped $table"
        else
            log_warn "Could not drop $table"
        fi
    done
    log ""
fi

log "Found ${#sql_files[@]} SQL files:"
log ""

for f in "${sql_files[@]}"; do
    name="$(basename "$f")"
    log "Executing $name..."
    # ON CLUSTER DDL returns a per-host status row. Capture it so a successful
    # run stays readable, and surface it only when something went wrong.
    if ! output="$(ch_exec "$(render "$f")" 2>&1)"; then
        log_err "$name failed"
        [ -n "$output" ] && printf '%s\n' "$output" >&2
        exit 1
    fi
    log_ok "$name executed successfully"
done

log ""
log "========================================"
log_ok "All tables created successfully"
log "========================================"
