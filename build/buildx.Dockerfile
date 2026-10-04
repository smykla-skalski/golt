# syntax=docker/dockerfile:1.26@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
FROM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190

ARG TARGETPLATFORM

# related to https://github.com/golangci/golangci-lint/issues/3107
ENV GOROOT /usr/local/go

# Allow to download a more recent version of Go.
# https://go.dev/doc/toolchain
# GOTOOLCHAIN=auto is shorthand for GOTOOLCHAIN=local+auto
ENV GOTOOLCHAIN auto

# Set all directories as safe
RUN git config --global --add safe.directory '*'

COPY $TARGETPLATFORM/golangci-lint /usr/bin/
CMD ["golangci-lint"]
