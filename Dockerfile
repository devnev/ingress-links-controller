FROM golang:1.23-alpine AS builder

WORKDIR /build

COPY / ./

RUN \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -v -o /controller .

FROM alpine

COPY --from=builder  /controller /usr/bin/controller

ENTRYPOINT [ "/usr/bin/controller" ]
