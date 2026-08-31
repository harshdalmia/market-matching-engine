# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first so the module cache survives source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# Static binary: the runtime image has no libc to link against.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/engine ./cmd
RUN mkdir -p /out/data

# ---------------------------------------------------------------------------
# Runtime stage
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Keep the runtime layout stable across hosts. Render can mount a persistent
# disk at /app/data, and relative WAL paths will also resolve under /app.
COPY --chown=65532:65532 --from=build /out /app

# PORT and ALLOWED_ORIGINS are read at startup. Leaving ALLOWED_ORIGINS unset
# allows any origin, which is fine for a public demo but should be pinned to
# the frontend's domain in production.
ENV PORT=8080

EXPOSE 8080
USER nonroot:nonroot

# No shell in distroless, so container-level HEALTHCHECK is omitted.
# Point your platform's HTTP probe at GET /health instead.
ENTRYPOINT ["/app/engine"]
