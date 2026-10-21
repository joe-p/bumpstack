# bumpstack

Bump container images in a docker-compose file to the newest matching version
and pin them to their multi-arch manifest digest.

`bumpstack` is for compose files where you want reproducible, digest-pinned
images but still want to track upstream releases. You declare *which* tags an
image is allowed to follow with a small comment, and `bumpstack` finds the
newest tag matching that pattern, resolves its manifest digest and, with
`--write`, rewrites the line in place.

```yaml
services:
  gluetun:
    # version: ^v3.\d+$
    image: qmcgaw/gluetun:v3.41@sha256:6b0f...   # -> bumped to the newest v3.x
```

## How it works

An `image:` mapping is **managed** when the comment directly above it holds a
version pattern:

```yaml
# version: <regex>
image: <reference>
```

* The pattern is a regular expression matched against each tag the registry
  advertises. Only matching tags are considered, so `^v3.\d+$` never jumps to a
  `v4` release.
* The line above may be separated by other comment lines, but not by blank
  lines or other keys.
* Images without a `# version:` pattern are left completely untouched.
* The file is edited line by line rather than parsed and re-serialised, so
  comments, key order, quoting, indentation, trailing comments and line endings
  are all preserved.

### Choosing a version

When several tags match, `--order` decides the winner:

| Mode | Behaviour |
| --- | --- |
| `version` (default) | Newest version by natural ordering, regardless of when it was pushed. |
| `pushed` | Newest pushed tag. Falls back to newest version when the registry cannot report push times. |

Natural ordering compares runs of digits numerically, so `v3.10` sorts after
`v3.9` and build counters like `1.2.3-r4` behave as expected.

`version` is the default because it is both the predictable choice and the
cheap one: on a registry that does not expose push times, `pushed` has to read
each matching tag's image config before it can rank them (see
[Registries](#registries)).

### Digest pinning

The digest written into the file always comes from a manifest request on the
chosen tag, whose `Docker-Content-Digest` is the digest of the **multi-arch
manifest list/index** when one exists. That means a pinned image keeps working
on every architecture the publisher supports.

If a tag only resolves to a single-architecture manifest (or an index covering
one platform), `bumpstack` prints a warning and still pins it, so the build does
not silently break on other architectures. Pass `--require-multiarch` to treat
that as a failure instead.

### Registries

* **Docker Hub** is queried through its REST API, which exposes a push
  timestamp per tag and is ordered by most recently updated.
* **Every other registry** uses the OCI `/v2/<name>/tags/list` endpoint with the
  standard bearer-token handshake. Push times are not part of the protocol, so
  the image config `created` timestamp is used as a best-effort proxy. Reading
  it costs a couple of requests per matching tag, so it only happens under
  `--order pushed`, only when at most 100 tags match, and with a few tags
  probed in parallel. Under the default `--order version` no probing is needed
  at all.

## Install

Requires Go 1.25 or newer.

```sh
go build -o bumpstack .
```

Or install straight from the module:

```sh
go install github.com/joe-p/bumpstack@latest
```

## Usage

```
bumpstack [OPTIONS] [FILE]      FILE defaults to docker-compose.yml
```

| Flag | Description |
| --- | --- |
| `-w`, `--write` | Apply the changes to the file. Without it `bumpstack` only reports what it would change (dry run). |
| `--check` | Write nothing; exit 1 if any image needs updating. Useful in CI. |
| `-s`, `--service NAME` | Only process this service; repeatable. |
| `--order version\|pushed` | Pick the newest version (default) or the newest pushed tag. |
| `--require-multiarch` | Fail instead of pinning a single-architecture tag. |
| `-j`, `--concurrency N` | Registry requests to issue in parallel (default 4). |
| `--insecure` | Use plain HTTP, for local test registries. |
| `-v`, `--verbose` | Extra diagnostics, including skipped images. |
| `-h`, `--help` | Print help. |
| `--version` | Print the version. |

Flags may appear before or after the file argument. `bumpstack` is a dry run by
default: it prints the changes it would make and leaves the file untouched
unless `--write` is given.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Updates are available (only under `--check`). |
| `2` | Failure (bad arguments, unreadable file, registry error, …). |

### Examples

```sh
# See what would change without touching the file (the default)
bumpstack

# Apply the changes in place
bumpstack --write docker-compose.yml

# Fail CI when an image is behind
bumpstack --check

# Track the most recently pushed tags, only these services, more parallelism
bumpstack --order pushed -s api -s worker -j 8 --write
```

## Credentials

Registry credentials are read from the Docker CLI configuration
(`$DOCKER_CONFIG/config.json`, or `~/.docker/config.json`). Inline `auth` and
`username`/`password` entries are supported, as are `credsStore` and
`credHelpers`, which shell out to the matching `docker-credential-*` helper. If
you can `docker login` to a private registry, `bumpstack` can reach it too.

Any failure to read the config is treated as "no credentials" rather than an
error.

## Development

```sh
go test ./...
go vet ./...
```

### Layout

| Path | Purpose |
| --- | --- |
| `main.go` | CLI: argument parsing, orchestration, reporting. |
| `internal/compose` | Line-oriented docker-compose reader/writer. |
| `internal/reference` | OCI/Docker image reference parsing. |
| `internal/registry` | Tag listing and manifest digest resolution. |
| `internal/ordering` | Natural ("human") ordering of tag strings. |
| `internal/dockerconfig` | Loading credentials from the Docker config. |
| `internal/updater` | Deciding and applying the update for each image. |
