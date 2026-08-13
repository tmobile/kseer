#!/bin/bash

# Test script for optimizing ClickHouse queries for large clusters
# Run this to test different query strategies before implementing in the UI

CLICKHOUSE_URL="${CLICKHOUSE_URL:-http://localhost:8123}"
CLICKHOUSE_USER="${CLICKHOUSE_USER:-default}"
CLICKHOUSE_PASSWORD="${CLICKHOUSE_PASSWORD:-}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Get time range (default: last 1 hour)
START_TIME=${1:-$(date -u -v-1H '+%Y-%m-%d %H:%M:%S' 2>/dev/null || date -u -d '1 hour ago' '+%Y-%m-%d %H:%M:%S')}
END_TIME=${2:-$(date -u '+%Y-%m-%d %H:%M:%S')}

echo -e "${BLUE}Testing ClickHouse Queries${NC}"
echo "Time range: $START_TIME to $END_TIME"
echo "======================================"
echo ""

# Function to test a query
test_query() {
    local name="$1"
    local query="$2"
    
    echo -e "${YELLOW}Testing: $name${NC}"
    echo "Query: $query" | head -c 200
    echo "..."
    echo ""
    
    # Run query and time it
    START=$(date +%s.%N)
    RESPONSE=$(curl -s -w "\n%{http_code}\n%{size_download}" -X POST "$CLICKHOUSE_URL" \
        -u "$CLICKHOUSE_USER:$CLICKHOUSE_PASSWORD" \
        --data-binary "$query FORMAT JSON")
    END=$(date +%s.%N)
    
    # Parse response
    HTTP_CODE=$(echo "$RESPONSE" | tail -n 2 | head -n 1)
    SIZE=$(echo "$RESPONSE" | tail -n 1)
    BODY=$(echo "$RESPONSE" | head -n -2)
    
    # Calculate duration
    DURATION=$(echo "$END - $START" | bc)
    
    # Check for errors
    if [ "$HTTP_CODE" -ne 200 ]; then
        echo -e "${RED}❌ FAILED (HTTP $HTTP_CODE)${NC}"
        echo "$BODY" | jq '.' 2>/dev/null || echo "$BODY"
        echo ""
        return 1
    fi
    
    # Count rows
    ROW_COUNT=$(echo "$BODY" | jq '.data | length' 2>/dev/null || echo "unknown")
    
    # Format size
    SIZE_MB=$(echo "scale=2; $SIZE / 1024 / 1024" | bc)
    
    echo -e "${GREEN}✅ SUCCESS${NC}"
    echo "  Duration: ${DURATION}s"
    echo "  Rows: $ROW_COUNT"
    echo "  Size: ${SIZE_MB} MB"
    echo ""
    
    return 0
}

echo "======================================"
echo -e "${BLUE}Query 1: Current Approach (Simple Scan with LIMIT)${NC}"
echo "======================================"
QUERY1="SELECT timestamp, cluster_name, namespace, name, kind, uid, operation, JSONExtractString(object, 'metadata', 'labels') as labels, JSONExtractString(object, 'metadata', 'annotations') as annotations, JSONExtractString(object, 'metadata', 'ownerReferences') as owner_references FROM kseer.kubernetes_events WHERE timestamp >= '$START_TIME' AND timestamp <= '$END_TIME' ORDER BY timestamp DESC LIMIT 500000"
test_query "Simple Scan with LIMIT" "$QUERY1"

echo "======================================"
echo -e "${BLUE}Query 2: Latest Event Per UID (Subquery Join)${NC}"
echo "======================================"
QUERY2="SELECT t.timestamp, t.cluster_name, t.namespace, t.name, t.kind, t.uid, t.operation, JSONExtractString(t.object, 'metadata', 'labels') as labels, JSONExtractString(t.object, 'metadata', 'annotations') as annotations, JSONExtractString(t.object, 'metadata', 'ownerReferences') as owner_references FROM kseer.kubernetes_events t INNER JOIN (SELECT uid, max(timestamp) as max_time FROM kseer.kubernetes_events WHERE timestamp >= '$START_TIME' AND timestamp <= '$END_TIME' GROUP BY uid) latest ON t.uid = latest.uid AND t.timestamp = latest.max_time WHERE t.operation != 'DELETED'"
test_query "Subquery Join" "$QUERY2"

echo "======================================"
echo -e "${BLUE}Query 3: GROUP BY with argMax${NC}"
echo "======================================"
QUERY3="SELECT uid, max(timestamp) as timestamp, argMax(cluster_name, timestamp) as cluster_name, argMax(namespace, timestamp) as namespace, argMax(name, timestamp) as name, argMax(kind, timestamp) as kind, argMax(operation, timestamp) as operation, argMax(JSONExtractString(object, 'metadata', 'labels'), timestamp) as labels, argMax(JSONExtractString(object, 'metadata', 'annotations'), timestamp) as annotations, argMax(JSONExtractString(object, 'metadata', 'ownerReferences'), timestamp) as owner_references FROM kseer.kubernetes_events WHERE timestamp >= '$START_TIME' AND timestamp <= '$END_TIME' GROUP BY uid HAVING argMax(operation, timestamp) != 'DELETED'"
test_query "GROUP BY with argMax" "$QUERY3"

echo "======================================"
echo -e "${BLUE}Query 4: Filtered scan (no deleted, smaller limit)${NC}"
echo "======================================"
QUERY4="SELECT timestamp, cluster_name, namespace, name, kind, uid, operation, JSONExtractString(object, 'metadata', 'labels') as labels, JSONExtractString(object, 'metadata', 'annotations') as annotations, JSONExtractString(object, 'metadata', 'ownerReferences') as owner_references FROM kseer.kubernetes_events WHERE timestamp >= '$START_TIME' AND timestamp <= '$END_TIME' AND operation != 'DELETED' ORDER BY timestamp DESC LIMIT 100000"
test_query "Filtered Scan (100k limit)" "$QUERY4"

echo ""
echo "======================================"
echo -e "${GREEN}Testing Complete!${NC}"
echo "======================================"
echo ""
echo "Recommendations:"
echo "1. Choose the query with the best balance of:"
echo "   - Small response size (fewer rows, less MB)"
echo "   - Fast execution time"
echo "   - No errors/timeouts"
echo ""
echo "2. The 'Simple Scan with LIMIT' is reliable but may return duplicate UIDs"
echo "3. The 'GROUP BY' approaches deduplicate but may be slower for large datasets"
echo "4. Test with your actual cluster size to see what works best"
echo ""


