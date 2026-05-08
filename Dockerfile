ARG GOLANG_VERSION=1.22

FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:${GOLANG_VERSION} AS builder

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

ARG Version
ARG GitCommit

ENV CGO_ENABLED=0
ENV GO111MODULE=on

RUN mkdir -p /go/src/github.com/the-i-engineers/cert-manager-webhook-oci
WORKDIR /go/src/github.com/the-i-engineers/cert-manager-webhook-oci

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY pkg  pkg
COPY main.go main.go
COPY main_test.go main_test.go

RUN CGO_ENABLED=${CGO_ENABLED} GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -ldflags "-s -w -X github.com/the-i-engineers/cert-manager-webhook-oci/pkg/version.Release=${Version} -X github.com/the-i-engineers/cert-manager-webhook-oci/pkg/version.SHA=${GitCommit}" -o /usr/bin/cert-manager-webhook-oci .

FROM --platform=${BUILDPLATFORM:-linux/amd64} gcr.io/distroless/base:nonroot

LABEL org.opencontainers.image.source=https://github.com/the-i-engineers/cert-manager-webhook-oci
LABEL org.opencontainers.image.description="cert-manager ACME DNS-01 webhook solver for Oracle Cloud Infrastructure DNS"
LABEL org.opencontainers.image.licenses=Apache-2.0

WORKDIR /
COPY --from=builder /usr/bin/cert-manager-webhook-oci /
USER nonroot:nonroot

ENTRYPOINT ["/cert-manager-webhook-oci"]
