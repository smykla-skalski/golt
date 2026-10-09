# golt

golt is a fork of [golangci-lint](https://github.com/golangci/golangci-lint). It has the same CLI, linters and configuration as golangci-lint. It is faster on large Go projects.

## Differences

- golt uses patched copies of `golang.org/x/tools`, `honnef.co/go/tools` and `revgrep`. The patches are in `forks/`.
- golt sets the GC percent to 400. If `GOMEMLIMIT` is not set, golt sets it to half of the RAM. To use the Go defaults, set `GOLT_GC=default` or `GOGC`.
- golt keeps a cache of `go list` results. To stop the cache, set `GOLT_LIST_CACHE=0`.
- `GOLT_DEPS_FACTS=light` or `GOLT_DEPS_FACTS=project` makes cold runs faster. With these values, golt can miss some findings in code that calls dependencies.
- `GOLT_DAEMON=1` keeps a background process for repeated `run` commands on Linux and macOS.
- `GOLT_ADMISSION=1` enables experimental machine-wide admission. Runs with a fresh `go list` cache snapshot use one of three slots; other runs use two. The default serial lock remains in use when this is unset.

## Install

Download the release for your system. Releases are available for Linux and macOS, on amd64 and arm64.

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
dir=$(mktemp -d)
gh release download --repo smykla-skalski/golt --pattern "*-${os}-${arch}.tar.gz" --output - | tar -xzf - --strip-components=1 -C "${dir}"
install "${dir}/golangci-lint" /usr/local/bin/
```

To build from source, use Go 1.26 or later and run `make build`.

## Use

Use golt as you use golangci-lint. The binary name is `golangci-lint`. For the documentation, refer to [golangci-lint.run](https://golangci-lint.run).

## Release

A tag with the form `vX.Y.Z-golt.N` starts the `Fork release` workflow. This workflow attaches the Linux and macOS archives to a GitHub release.

## Upstream sync

The `Sync upstream` workflow merges `golangci/golangci-lint` `main` into golt each day. To rebase the patched modules, refer to `forks/README.md`.

## License

GPL-3.0, the same as golangci-lint. Refer to `LICENSE`.
