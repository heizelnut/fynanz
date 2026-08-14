# Multi-stage build (AGENTS.md §9: one container, minimal moving parts).
# CGO_ENABLED=0: modernc.org/sqlite is pure Go, so the binary is fully static.

# ---- build ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Layer-cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fynanz ./cmd/server

# ---- run ----
FROM alpine:3.21
# ca-certificates: Enable Banking / ntfy TLS verification. tzdata: not
# strictly needed (the app is UTC) but harmless for future features.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 fynanz

WORKDIR /app
COPY --from=build /out/fynanz /app/fynanz

# SQLite lives on the /data volume; ownership matters for the first mount.
RUN mkdir -p /data && chown -R fynanz:fynanz /data /app
USER fynanz

EXPOSE 8080
VOLUME /data

HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/fynanz"]
