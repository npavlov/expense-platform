# Build context: repository root, e.g.
#   docker build -f tools/devidp/Dockerfile -t expense-platform/devidp .
# Base images are pinned by digest; Dependabot proposes updates.
FROM golang:1.26.3-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
# Dependencies first, so code changes do not invalidate the module cache layer.
COPY libs/platform/go.mod libs/platform/go.sum libs/platform/
COPY tools/devidp/go.mod tools/devidp/go.sum tools/devidp/
RUN --mount=type=cache,target=/root/go/pkg/mod cd tools/devidp && go mod download && go mod verify
COPY libs/platform/ libs/platform/
COPY tools/devidp/ tools/devidp/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd tools/devidp && go build -ldflags="-s -w" -o /out/devidp .

# distroless/static: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/devidp"]
