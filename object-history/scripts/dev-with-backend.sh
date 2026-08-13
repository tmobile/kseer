#!/bin/bash

set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${GREEN}Starting Kseer Frontend with Backend...${NC}"

# Check if backend is running
if ! docker ps | grep -q "kseer-druid-router"; then
    echo -e "${YELLOW}Backend not running. Starting backend services...${NC}"
    cd ../../kseer-backend
    ./scripts/setup.sh
    cd ../kseer-frontend
    echo -e "${GREEN}Waiting for backend to be ready...${NC}"
    sleep 30
fi

# Install dependencies if needed
if [ ! -d "node_modules" ]; then
    echo -e "${GREEN}Installing dependencies...${NC}"
    npm install
fi

# Start the dev server
echo -e "${GREEN}Starting frontend development server...${NC}"
echo -e "${YELLOW}The frontend will be available at http://localhost:3000${NC}"
echo -e "${YELLOW}Druid Console: http://localhost:8888${NC}"
echo -e "${YELLOW}Kafka UI: http://localhost:8080${NC}"

npm run dev
