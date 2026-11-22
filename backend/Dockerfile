# syntax=docker/dockerfile:1.7
# BTN Operations API — static Go binaries on a distroless base (no shell, non-root).
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# timetzdata embeds the zone database so Africa/Kigali resolves without OS tzdata.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -tags timetzdata -ldflags="-s -w" -o /out/ \
      ./cmd/server ./cmd/migrate ./cmd/seed ./cmd/import

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ /app/
ENV APP_ENV=production PORT=4000
EXPOSE 4000
USER nonroot:nonroot
# Migrations run in the separate "migrate" service (see docker-compose.yml) before the API starts.
ENTRYPOINT ["/app/server"]
