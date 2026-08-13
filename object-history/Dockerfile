# Build stage
FROM node:18-alpine as builder

# Install base certificate tooling.
RUN apk add --no-cache ca-certificates wget

# Optional: add a corporate root CA certificate if you build behind a TLS-inspecting proxy.
# Provide the CA URL at build time, e.g.:
#   docker build --build-arg CORP_CA_URL=https://ca.example.com/root-ca.pem .
ARG CORP_CA_URL=""
RUN if [ -n "$CORP_CA_URL" ]; then \
      mkdir -p /usr/local/share/ca-certificates && \
      wget -cO /usr/local/share/ca-certificates/corp-root-ca.crt "$CORP_CA_URL" && \
      update-ca-certificates; \
    fi

# Set SSL environment variable for Node.js
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
RUN npm ci

# Copy source code
COPY . .

# Build the application
RUN npm run build

# Production stage
FROM nginx:alpine

# Copy built assets from builder
COPY --from=builder /app/dist /usr/share/nginx/html

# Copy nginx configuration
COPY nginx.conf /etc/nginx/nginx.conf

# Expose port
EXPOSE 80

# Start nginx
CMD ["nginx", "-g", "daemon off;"]
