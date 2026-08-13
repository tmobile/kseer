# Build stage
FROM golang:1.25-alpine AS builder

# Optional: add a corporate root CA certificate if you build behind a TLS-inspecting proxy.
# Provide the CA URL at build time, e.g.:
#   docker build --build-arg CORP_CA_URL=https://ca.example.com/root-ca.pem .
ARG CORP_CA_URL=""
RUN if [ -n "$CORP_CA_URL" ]; then \
      mkdir -p /usr/local/share/ca-certificates && \
      wget -cO /usr/local/share/ca-certificates/corp-root-ca.crt "$CORP_CA_URL" && \
      cat /usr/local/share/ca-certificates/corp-root-ca.crt >> /etc/ssl/certs/ca-certificates.crt; \
    fi

# Install build dependencies
RUN apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o kseer-agent cmd/agent/main.go

# Final stage
FROM alpine:3.18

# Copy ca-certificates from builder (includes any corporate root CA added above)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Install runtime dependencies
RUN apk add --no-cache tzdata

# Set SSL environment variables
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt

# Create non-root user
RUN addgroup -g 1000 kseer && \
    adduser -D -u 1000 -G kseer kseer

# Copy binary from builder
COPY --from=builder /app/kseer-agent /usr/local/bin/kseer-agent

# Set ownership
RUN chown -R kseer:kseer /usr/local/bin/kseer-agent && \
    chmod +x /usr/local/bin/kseer-agent

# Switch to non-root user
USER kseer

# Set entrypoint
ENTRYPOINT ["kseer-agent"]
