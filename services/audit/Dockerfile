# Build context: repository root, e.g.
#   docker build -f services/audit/Dockerfile -t expense-platform/audit .
# Base images are pinned by digest; Dependabot proposes updates.
FROM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
# Dependencies first, so code changes do not invalidate the module cache layer.
COPY libs/platform/go.mod libs/platform/go.sum libs/platform/
COPY services/audit/go.mod services/audit/go.sum services/audit/
RUN --mount=type=cache,target=/root/go/pkg/mod cd services/audit && go mod download && go mod verify
COPY libs/platform/ libs/platform/
COPY services/audit/ services/audit/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd services/audit && go build -ldflags="-s -w" -o /out/audit ./cmd/audit && go build -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# distroless/static: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
EXPOSE 8080 8081 9090
ENTRYPOINT ["/usr/local/bin/audit"]
