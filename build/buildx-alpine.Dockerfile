# syntax=docker/dockerfile:1.26@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414

ARG TARGETPLATFORM

# related to https://github.com/golangci/golangci-lint/issues/3107
ENV GOROOT /usr/local/go

# Allow to download a more recent version of Go.
# https://go.dev/doc/toolchain
# GOTOOLCHAIN=auto is shorthand for GOTOOLCHAIN=local+auto
ENV GOTOOLCHAIN auto

# gcc is required to support cgo;
# git and mercurial are needed most times for go get`, etc.
# See https://github.com/docker-library/golang/issues/80
RUN apk --no-cache add gcc musl-dev git mercurial

# Set all directories as safe
RUN git config --global --add safe.directory '*'

COPY $TARGETPLATFORM/golangci-lint /usr/bin/
CMD ["golangci-lint"]
