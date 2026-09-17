# syntax=docker/dockerfile:1

# ============================================================
# Stage 1: Build the static frontend once for every target platform.
# ============================================================
FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend-builder

ARG BUILD_VERSION=dev

WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund
COPY frontend/ .
RUN VITE_APP_VERSION=${BUILD_VERSION} npm run build

# ============================================================
# Stage 2: Cross-compile the backend on the build platform.
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS go-builder

ARG TARGETARCH
ARG BUILD_VERSION=dev

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
COPY --from=frontend-builder /frontend/dist ./frontend/dist

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w -X github.com/codex2api/internal/version.Version=${BUILD_VERSION}" -o /codex2api .

# ============================================================
# Stage 3: Minimal runtime image.
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=go-builder /codex2api /usr/local/bin/codex2api

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/codex2api"]
