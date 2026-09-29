# Forked dependencies

golt builds golangci-lint against patched copies of these modules. The root
`go.mod` replaces each module with its directory here, so builds and tests
always use the patched code.

`UPSTREAM` lists, per line: module path, upstream version the directory is
based on, directory, upstream repository. The `fork-deps` check in
`.github/workflows/fork-release.yml` fails when the root `go.mod` requires a
different version than the one listed, i.e. when an upstream sync bumped a
dependency the fork has not been rebased onto yet.

Each directory is a squashed `git subtree` of the upstream tag, followed by
our patches as ordinary commits (`git log -- forks/<dir>`).

## Rebase a fork onto a new upstream version

```sh
# e.g. golang.org/x/tools v0.51.0
git fetch --depth 1 --no-tags https://go.googlesource.com/tools \
  refs/tags/v0.51.0:refs/tags/upstream-tools/v0.51.0
git subtree merge --prefix=forks/tools --squash -S upstream-tools/v0.51.0 \
  -m "build(forks): update x/tools to v0.51.0"
# resolve conflicts in forks/tools, then update the version in UPSTREAM
go mod tidy && go test ./... && (cd forks/tools && go test ./go/ssa/... ./go/packages/...)
```

## Upstream contributions

Patches worth upstreaming are sent from a separate checkout of the upstream
repository; keep this directory the source of truth until they merge.
