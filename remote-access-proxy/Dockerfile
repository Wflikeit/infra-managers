# SPDX-FileCopyrightText: (C) 2025 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.25.5-bookworm as build

ENV GO111MODULE=on
ARG MAKE_TARGET=go-build
# Arguments used to stamp the binary
ARG REPO_URL
ARG VERSION
ARG REVISION
ARG BUILD_DATE

COPY common.mk version.mk /go/src/github.com/open-edge-platform/infra-managers/
COPY Makefile go.mod go.sum /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy/
COPY cmd/ /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy/cmd/
COPY internal/ /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/
COPY pkg/ /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy/pkg/
COPY vendor/ /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy/vendor/

WORKDIR /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy

RUN CGO_ENABLED=0 LABEL_REPO_URL=${REPO_URL} LABEL_VERSION=${VERSION} LABEL_REVISION=${REVISION} LABEL_BUILD_DATE=${BUILD_DATE} make ${MAKE_TARGET}

# Use base distroless image (not static) because RAP needs networking tools for Chisel and SSH
FROM gcr.io/distroless/base-debian12:nonroot

# Run as non-privileged user
USER nobody

ARG REPO_URL
ARG VERSION
ARG REVISION
ARG BUILD_DATE

LABEL org.opencontainers.image.version=${VERSION} \
      org.opencontainers.image.source=${REPO_URL} \
      org.opencontainers.image.revision=${REVISION} \
      org.opencontainers.image.created=${BUILD_DATE}

# Copy the built binary
COPY --from=build --chown=nobody:nobody /go/src/github.com/open-edge-platform/infra-managers/remote-access-proxy/out/rmtaccessproxy /usr/local/bin/rmtaccessproxy

# RAP requires:
# - Port 8080: Chisel server endpoint (for agent connections)
# - Port 8000: Reverse SSH tunnel endpoint
EXPOSE 8080 8000

ENTRYPOINT ["rmtaccessproxy"]
