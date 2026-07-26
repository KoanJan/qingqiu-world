# Stage 1: Build the web frontend
FROM node:22-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build the Go server
FROM golang:1.26-alpine AS server-builder
WORKDIR /server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o qingqiu-world-server ./cmd/

# Stage 3: Runtime with nginx
FROM alpine:3.21
RUN apk add --no-cache nginx ca-certificates tzdata

WORKDIR /app
COPY --from=server-builder /server/qingqiu-world-server .
COPY --from=web-builder /web-dist ./web-dist

COPY docker/nginx.conf /etc/nginx/http.d/default.conf
COPY docker/docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

EXPOSE 18888
ENV DATA_ROOT=/app/data
ENV LOG_LEVEL=INFO
CMD ["/docker-entrypoint.sh"]
