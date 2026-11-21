# Build context: repository root, e.g.
#   docker build -f services/expenses/Dockerfile -t expense-platform/expenses .
# Base images are pinned by digest; Dependabot proposes updates.
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
# Dependencies first, so code changes do not invalidate the module cache layer.
COPY libs/platform/go.mod libs/platform/go.sum libs/platform/
COPY services/expenses/go.mod services/expenses/go.sum services/expenses/
RUN --mount=type=cache,target=/root/go/pkg/mod cd services/expenses && go mod download && go mod verify
COPY libs/platform/ libs/platform/
COPY services/expenses/ services/expenses/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd services/expenses && go build -ldflags="-s -w" -o /out/expenses ./cmd/expenses && go build -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# distroless/static: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/expenses"]
