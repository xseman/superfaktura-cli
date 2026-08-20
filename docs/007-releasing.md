# 007 · Releasing

Nobody types a version number. A merge decides it.

```
 conventional commits on main
            │
            ▼
 release-please  ──── opens/updates a release PR ("v0.2.0")
            │          CHANGELOG.md · .github/.release-manifest.json
            │          .claude-plugin/plugin.json
            │
        merge it
            │
            ▼
 tag v0.2.0 + GitHub release        ← release-please, with a PAT
            │
            ▼
 goreleaser ──── 6 binaries, archives, checksums, appended to that release
```

Both halves live in `.github/workflows/release.yml`, one job each. The second
job runs only when the first reports `release_created`.

## What owns what

| Thing                     | Owner          | Where                                                 |
| ------------------------- | -------------- | ----------------------------------------------------- |
| The version number        | release-please | `.github/.release-manifest.json`, bumped by the PR    |
| `CHANGELOG.md`, the notes | release-please | `changelog: disable: true` keeps goreleaser out of it |
| The tag and the release   | release-please | which is why goreleaser runs `release.mode: append`   |
| The binaries and archives | goreleaser     | `.goreleaser.yaml`                                    |

The bump follows the commit types: `feat:` minor, `fix:`/`perf:` patch, `!` or
`BREAKING CHANGE` major. `bump-minor-pre-major` keeps a breaking change below
1.0 at a minor bump. A commit type outside `.github/.release-config.json`'s
`changelog-sections` still lands, it just does not show up in the notes.

## Rules

- **The archive name is an interface.** `scripts/install.sh` builds
  `sf_<version>_<os>_<arch>.tar.gz` by hand. Change `name_template` and the
  installer stops finding anything — 404, not a build failure.
- **The version reaches the binary only through the tag.** `-X
  main.version={{.Version}}`; `cmd/sf/main.go` falls back to the module version
  a `go install` build stamps, and to `dev` when there is neither.
- **`RELEASE_PLEASE_TOKEN` is a PAT on purpose.** A release created with
  `GITHUB_TOKEN` triggers no workflows, so CI would never run on the release PR.

## Not set up

- **Homebrew.** The `brews:` block was removed: `xseman/homebrew-tap` does not
  exist, and it called `generate_completions_from_executable`, which needs an
  `sf completion` command this CLI does not have. Both are prerequisites, not
  config.
- **deb/rpm.** Add an `nfpms:` block to `.goreleaser.yaml` when somebody asks.
- **Windows packaging.** `scripts/install.sh` points Windows users at
  `winget install xseman.superfaktura-cli`, which does not exist. The release
  ZIP does.

## First release

The manifest starts at `0.0.0`, so the first release PR is `v0.1.0`.
