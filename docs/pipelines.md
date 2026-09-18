# Pipelines

The pipelines are a [Dagger](https://dagger.io) module in `.dagger/` (Go SDK). The same checks run on
a laptop and in CI, so a green `make check` means a green CI run.

| Check | What it does |
|---|---|
| `lint` | `make lint` (golangci-lint) and `helm lint` on the chart |
| `test` | `make test`: unit tests and envtest |
| `end-to-end` | kind cluster in Docker-in-Docker → images built from the tree → `test/e2e` → cluster discarded |

## Running locally

The Dagger CLI comes with the Nix dev shell (`nix develop` / direnv). Dagger starts its engine as a
container, so Docker (or another container runtime) must be running.

```sh
make check                     # all checks, in parallel (dagger check)
make test-e2e                  # only end-to-end (dagger call end-to-end)
dagger check -l                # list checks
dagger check tigerbeetle-operator:lint
```

Checks only see the working tree, including uncommitted changes, minus the paths ignored in
`.dagger/main.go` (`bin`, `.git`, …).

### Debugging a failing e2e run

- The suite prints the TigerBeetleCluster, pods, replica logs, events and operator logs for a failing spec.
- `dagger call -i end-to-end` opens a shell in the failing container, with `kubectl`, `helm`, `kind`
  and `docker` pointed at the still-running cluster.
- For iterative work on the operator itself, use the Tilt cluster (`make kind-up tilt-up`); the e2e
  cluster lives only for one run.

## How the e2e cluster works

```
Dagger engine
├── dockerd service (docker:dind, privileged, hostname "docker")
│   └── kind nodes: control plane + WORKERS workers
│       └── API server published on docker:6443
└── runner container (Go, docker CLI, kind, kubectl, helm)
    ├── kind create cluster (DOCKER_HOST=tcp://docker:2375)
    ├── kubeconfig rewritten to https://docker:6443 (added to the API server certificate)
    ├── docker load + kind load: operator and init images (docker/Dockerfile, docker/Dockerfile.init),
    │   plus the TigerBeetle and sidecar images pulled and cached by Dagger
    └── go test ./test/e2e (installs the Helm chart, runs the specs)
```

- **Teardown.** The Docker daemon is a Dagger service stopped when the check returns, pass or fail,
  so no cluster outlives the run and nothing is created on the host's Docker.
- **Caching.** Every step on the live cluster carries a cache buster, so it always runs. Image builds,
  tool downloads, Go module and build caches are cached by Dagger. Docker's storage is a cache volume
  (it cannot live on the container's overlay filesystem), which also keeps the kind node image
  between local runs; each run first deletes any cluster an interrupted run left behind.
- **Why Docker-in-Docker instead of the host's Docker socket.** It is self-contained: the same on a
  laptop and a CI runner, with no host state to clean up and no dependency on how the engine is
  networked. The cost is a privileged container (`insecureRootCapabilities`), limited to this service.

## CI

`.github/workflows/ci.yaml` runs every check in a single job with
[`dagger/dagger-for-github`](https://github.com/dagger/dagger-for-github), on pushes to `main` and on
pull requests. The checks run in parallel inside one Dagger engine and share its cache, so the Go
image, module downloads and build cache are fetched once per run rather than once per check; a job
per check would look tidier in the GitHub UI but start each one with a cold cache. Per-check results
are written to the job summary.

The Dagger version is pinned in three places that must move together: `engineVersion` in
`dagger.json`, `DAGGER_VERSION` in the workflow, and the `dagger/nix` input in `flake.lock`
(`nix flake update dagger`).

The e2e check is the heavy one: a kind control plane, three workers and three TigerBeetle replicas
with a 2Gi limit. The Dagger engine peaked at about 10 GiB during a local run (measured with
`docker stats`, which includes page cache, so this is an upper bound). A standard GitHub-hosted
Linux runner for public repositories has 16 GiB; check the memory of the runner you use.

## Releases

`.github/workflows/release.yaml` publishes both images and the Helm chart on a `v*` tag (or on
demand). It does not use Dagger: a release runs only in CI, where local parity buys little, while
Dagger's cold start costs minutes per run that GitHub cannot cache (the engine boot, and building
the Dagger module itself). `docker/build-push-action` stores layers in GitHub's cache instead, so
repeat releases skip the Go build.

- The tag becomes the image tag, the chart `version` and the chart `appVersion` (a leading `v` is
  stripped, since chart versions must be plain SemVer), so a released chart installs its own images.
  Both images are also tagged `latest`.
- Images are built for `linux/amd64` and `linux/arm64` without emulation: the Dockerfiles build on
  the native platform (`FROM --platform=$BUILDPLATFORM`) and cross-compile with `GOARCH`, and the
  final stage only copies the binary.
- Locally, `make docker-buildx IMG=...` runs the same buildx build; the chart is `helm package`
  plus `helm push`.

Measured locally, a cold multi-platform build of both images takes about two minutes, and a repeat
build with a warm cache is instant; the cache is what the CI job leans on.

## Documentation site

The pages in `docs/` are also a site, built with [mdBook](https://rust-lang.github.io/mdBook/)
(`book.toml`; navigation in `docs/SUMMARY.md`). `make docs-serve` serves it locally with live reload;
`make docs` builds it into `book/`.

- `docs/introduction.md` includes the top of `README.md`, and `docs/chart-values.md` includes the
  values table from the chart README, through mdBook anchors (`<!-- ANCHOR: ... -->`), so neither is
  written twice.
- `.github/workflows/docs.yaml` builds the site on pull requests that touch the docs, checks every
  internal link with [lychee](https://lychee.cli.rs/) (a broken link fails the build), and deploys it
  to GitHub Pages from `main`. External links are not checked, so an unreachable third-party site
  never blocks the docs.
- A page only appears in the site once it is listed in `docs/SUMMARY.md`.
