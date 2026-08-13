.PHONY: build test clean install helm-lint helm-package run

# Binary name
BINARY=kseer-agent
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}"

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build directories
BUILD_DIR=./build
HELM_CHART_DIR=./deploy/helm/kseer-agent

# Root CA to trust inside the image build, for TLS-inspecting corporate proxies.
# Without it, `apk add` in the builder stage fails with "server certificate not trusted".
CORP_CA_URL?=
DOCKER_BUILD_ARGS=$(if $(CORP_CA_URL),--build-arg CORP_CA_URL=$(CORP_CA_URL),)

all: test build

build:
	@echo "Building $(BINARY)..."
	$(GOBUILD) $(LDFLAGS) -o $(BINARY) ./cmd/agent/main.go
	@echo "Build complete: ./$(BINARY)"

build-linux:
	@echo "Building $(BINARY) for Linux..."
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BINARY)-linux-amd64 ./cmd/agent/main.go
	@echo "Build complete: ./$(BINARY)-linux-amd64"

build-darwin:
	@echo "Building $(BINARY) for Darwin..."
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BINARY)-darwin-amd64 ./cmd/agent/main.go
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BINARY)-darwin-arm64 ./cmd/agent/main.go
	@echo "Build complete: ./$(BINARY)-darwin-*"

build-all: build-linux build-darwin

test:
	@echo "Running tests..."
	$(GOTEST) -v -race -coverprofile=coverage.out ./...
	@echo "Tests complete"

test-coverage: test
	@echo "Generating coverage report..."
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -f $(BINARY)
	rm -f $(BINARY)-*
	rm -f coverage.out coverage.html
	rm -rf $(BUILD_DIR)
	@echo "Clean complete"

deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy
	@echo "Dependencies updated"

install: build
	@echo "Installing $(BINARY)..."
	cp $(BINARY) /usr/local/bin/$(BINARY)
	@echo "Installed to /usr/local/bin/$(BINARY)"

run:
	@echo "Running $(BINARY)..."
	$(GOBUILD) -o $(BINARY) ./cmd/agent/main.go
	./$(BINARY) --config=config.yaml

run-dev:
	@echo "Running $(BINARY) in development mode..."
	$(GOBUILD) -o $(BINARY) ./cmd/agent/main.go
	LOG_LEVEL=debug ./$(BINARY) --config=config.yaml

# Docker targets
docker-build:
	@echo "Building Docker image..."
	docker build $(DOCKER_BUILD_ARGS) -t kseer/kseer-agent:$(VERSION) .
	docker tag kseer/kseer-agent:$(VERSION) kseer/kseer-agent:latest
	@echo "Docker image built: kseer/kseer-agent:$(VERSION)"

docker-push:
	@echo "Pushing Docker image..."
	docker push kseer/kseer-agent:$(VERSION)
	docker push kseer/kseer-agent:latest

# Helm targets
helm-lint:
	@echo "Linting Helm chart..."
	helm lint $(HELM_CHART_DIR)
	@echo "Helm lint complete"

helm-template:
	@echo "Rendering Helm templates..."
	helm template kseer-agent $(HELM_CHART_DIR) --debug
	@echo "Template rendering complete"

helm-template-prod:
	@echo "Rendering Helm templates with production values..."
	helm template kseer-agent $(HELM_CHART_DIR) -f $(HELM_CHART_DIR)/values-examples/production.yaml --debug
	@echo "Template rendering complete"

helm-package:
	@echo "Packaging Helm chart..."
	mkdir -p $(BUILD_DIR)
	helm package $(HELM_CHART_DIR) -d $(BUILD_DIR)
	@echo "Helm chart packaged in $(BUILD_DIR)"

helm-install:
	@echo "Installing Helm chart..."
	helm install kseer-agent $(HELM_CHART_DIR) -n monitoring --create-namespace
	@echo "Helm chart installed"

helm-install-dev:
	@echo "Installing Helm chart with dev values..."
	helm install kseer-agent $(HELM_CHART_DIR) -n monitoring --create-namespace -f $(HELM_CHART_DIR)/values-examples/development.yaml
	@echo "Helm chart installed"

helm-uninstall:
	@echo "Uninstalling Helm chart..."
	helm uninstall kseer-agent -n monitoring
	@echo "Helm chart uninstalled"

helm-upgrade:
	@echo "Upgrading Helm chart..."
	helm upgrade kseer-agent $(HELM_CHART_DIR) -n monitoring
	@echo "Helm chart upgraded"

# Help target
help:
	@echo "Available targets:"
	@echo "  build           - Build the binary for current platform"
	@echo "  build-linux     - Build for Linux"
	@echo "  build-darwin    - Build for macOS (Intel and ARM)"
	@echo "  build-all       - Build for all platforms"
	@echo "  test            - Run tests"
	@echo "  test-coverage   - Run tests with coverage report"
	@echo "  clean           - Remove build artifacts"
	@echo "  deps            - Download and tidy dependencies"
	@echo "  install         - Install binary to /usr/local/bin"
	@echo "  run             - Build and run the agent"
	@echo "  run-dev         - Build and run with debug logging"
	@echo ""
	@echo "Docker targets:"
	@echo "  docker-build    - Build Docker image"
	@echo "  docker-push     - Push Docker image to registry"
	@echo ""
	@echo "Helm targets:"
	@echo "  helm-lint       - Lint Helm chart"
	@echo "  helm-template   - Render Helm templates"
	@echo "  helm-template-prod - Render with production values"
	@echo "  helm-package    - Package Helm chart"
	@echo "  helm-install    - Install Helm chart"
	@echo "  helm-install-dev - Install with development values"
	@echo "  helm-uninstall  - Uninstall Helm chart"
	@echo "  helm-upgrade    - Upgrade Helm chart"
