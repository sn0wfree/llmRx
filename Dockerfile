# syntax=docker/dockerfile:1.6

# ---- stage 1: build the React SPA ----
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci --silent
COPY web/ ./
RUN npm run build

# ---- stage 2: build the Go binary ----
FROM golang:1.22-alpine AS gobuild
WORKDIR /src
# Pre-cache deps for incremental builds.
COPY go.mod go.sum ./
RUN go mod download
# Copy the rest of the source.
COPY . .
# Stage the freshly built SPA into the embed path.
COPY --from=web /web/dist /src/internal/webui/dist
# CGO is required for the mattn/go-sqlite3 driver.
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /out/llmRx ./cmd/gateway

# ---- stage 3: distroless runtime ----
FROM gcr.io/distroless/static:nonroot
COPY --from=gobuild /out/llmRx /usr/local/bin/llmRx
USER nonroot:nonroot
EXPOSE 8787
VOLUME ["/data"]
ENV LLMRX_DB=/data/llmrx.db
ENTRYPOINT ["/usr/local/bin/llmRx", "-config", "/data/config.yml"]
