#!/bin/bash

# Test script for optimizing Druid queries for large clusters
# Run this to test different query strategies before implementing in the UI

DRUID_URL="${DRUID_URL:-http://localhost:8888/druid/v2/sql}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Get time range (default: last 1 hour)
START_TIME=${1:-$(date -u -v-1H '+%Y-%m-%d %H:%M:%S' 2>/dev/null || date -u -d '1 hour ago' '+%Y-%m-%d %H:%M:%S')}
END_TIME=${2:-$(date -u '+%Y-%m-%d %H:%M:%S')}

echo -e "${BLUE}Testing Druid Queries${NC}"
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
    RESPONSE=$(curl -s -w "\n%{http_code}\n%{size_download}" -X POST "$DRUID_URL" \
        -H "Content-Type: application/json" \
        -d "{\"query\": \"$query\", \"context\": {\"sqlTimeZone\": \"UTC\"}}")
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
    ROW_COUNT=$(echo "$BODY" | jq '. | length' 2>/dev/null || echo "unknown")
    
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
QUERY1="SELECT __time, cluster_name, namespace, name, kind, uid, operation, labels, annotations, owner_references FROM kubernetes_objects WHERE __time >= TIMESTAMP '$START_TIME' AND __time <= TIMESTAMP '$END_TIME' ORDER BY __time DESC LIMIT 500000"
test_query "Simple Scan with LIMIT" "$QUERY1"

echo "======================================"
echo -e "${BLUE}Query 2: Latest Event Per UID (Subquery Join)${NC}"
echo "======================================"
QUERY2="SELECT t.__time, t.cluster_name, t.namespace, t.name, t.kind, t.uid, t.operation, t.labels, t.annotations, t.owner_references FROM kubernetes_objects t INNER JOIN (SELECT uid, MAX(__time) as max_time FROM kubernetes_objects WHERE __time >= TIMESTAMP '$START_TIME' AND __time <= TIMESTAMP '$END_TIME' GROUP BY uid) latest ON t.uid = latest.uid AND t.__time = latest.max_time WHERE t.operation != 'DELETED'"
test_query "Subquery Join" "$QUERY2"

echo "======================================"
echo -e "${BLUE}Query 3: Window Function (LATEST_BY)${NC}"
echo "======================================"
QUERY3="SELECT DISTINCT uid, LATEST(__time) OVER (PARTITION BY uid) as __time, LATEST(cluster_name) OVER (PARTITION BY uid) as cluster_name, LATEST(namespace) OVER (PARTITION BY uid) as namespace, LATEST(name) OVER (PARTITION BY uid) as name, LATEST(kind) OVER (PARTITION BY uid) as kind, LATEST(operation) OVER (PARTITION BY uid) as operation, LATEST(labels) OVER (PARTITION BY uid) as labels, LATEST(annotations) OVER (PARTITION BY uid) as annotations, LATEST(owner_references) OVER (PARTITION BY uid) as owner_references FROM kubernetes_objects WHERE __time >= TIMESTAMP '$START_TIME' AND __time <= TIMESTAMP '$END_TIME' AND LATEST(operation) OVER (PARTITION BY uid) != 'DELETED'"
test_query "Window Function" "$QUERY3"

echo "======================================"
echo -e "${BLUE}Query 4: GROUP BY with LATEST aggregation${NC}"
echo "======================================"
QUERY4="SELECT MAX(__time) as __time, ANY_VALUE(cluster_name) as cluster_name, ANY_VALUE(namespace) as namespace, ANY_VALUE(name) as name, ANY_VALUE(kind) as kind, uid, LATEST(operation) as operation, LATEST(labels) as labels, LATEST(annotations) as annotations, LATEST(owner_references) as owner_references FROM kubernetes_objects WHERE __time >= TIMESTAMP '$START_TIME' AND __time <= TIMESTAMP '$END_TIME' GROUP BY uid HAVING LATEST(operation) != 'DELETED'"
test_query "GROUP BY with LATEST" "$QUERY4"

echo "======================================"
echo -e "${BLUE}Query 5: Simple GROUP BY (safer)${NC}"
echo "======================================"
QUERY5="SELECT uid, MAX(__time) as __time, ANY_VALUE(cluster_name) as cluster_name, ANY_VALUE(namespace) as namespace, ANY_VALUE(name) as name, ANY_VALUE(kind) as kind, ANY_VALUE(operation) as operation, ANY_VALUE(labels) as labels, ANY_VALUE(annotations) as annotations, ANY_VALUE(owner_references) as owner_references FROM kubernetes_objects WHERE __time >= TIMESTAMP '$START_TIME' AND __time <= TIMESTAMP '$END_TIME' GROUP BY uid"
test_query "Simple GROUP BY" "$QUERY5"

echo "======================================"
echo -e "${BLUE}Query 6: Filtered scan (no deleted, smaller limit)${NC}"
echo "======================================"
QUERY6="SELECT __time, cluster_name, namespace, name, kind, uid, operation, labels, annotations, owner_references FROM kubernetes_objects WHERE __time >= TIMESTAMP '$START_TIME' AND __time <= TIMESTAMP '$END_TIME' AND operation != 'DELETED' ORDER BY __time DESC LIMIT 100000"
test_query "Filtered Scan (100k limit)" "$QUERY6"

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
echo "3. The 'GROUP BY' approaches deduplicate but may hit memory limits"
echo "4. Test with your actual cluster size to see what works best"
echo ""


