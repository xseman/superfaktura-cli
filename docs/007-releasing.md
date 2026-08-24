# 007 · Releasing

Nobody types a version number. A merge decides it.

```
 conventional commits on master
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
 build job ──── 6 binaries, deb + rpm, CHECKSUMS.txt, uploaded to that release
```

Both halves live in `.github/workflows/release.yml`, one job each. The build
steps are inline in the workflow — six `go build`s on one runner (CGO is off),
`nfpm` for the Linux packages, `gh release upload` — there is no separate
release tool or config. The second job runs only when the first reports
`release_created`.

The bump follows the commit types: `feat:` minor, `fix:`/`perf:` patch, `!` or
`BREAKING CHANGE` major. `bump-minor-pre-major` keeps a breaking change below
1.0 at a minor bump. A commit type outside `.github/.release-config.json`'s
`changelog-sections` still lands, it just does not show up in the notes.

## Rules

- **The asset name is an interface.** `scripts/install.sh` downloads
  `sf-<os>-<arch>` and verifies it against `CHECKSUMS.txt`. Rename either and
  the installer stops finding anything — 404, not a build failure.
- **The version reaches the binary only through the tag.** `-X
  main.version=${TAG#v}`; `cmd/sf/main.go` falls back to the module version a
  `go install` build stamps, and to `dev` when there is neither.
- **`RELEASE_PLEASE_TOKEN` is a PAT on purpose.** A release created with
  `GITHUB_TOKEN` triggers no workflows, so CI would never run on the release PR.

## Not set up

- **Homebrew.** Needs a tap repository first, and a `completion` command if the
  formula is to generate completions.
- **Windows packaging.** The release carries a bare `sf-windows-<arch>.exe`;
  winget needs a manifest nobody has written.

## First release

`initial-version: 0.1.0` in `.github/.release-config.json` pins the first
release PR to `v0.1.0` — a manifest at `0.0.0` means "no release yet", and
release-please would otherwise bootstrap at `v1.0.0`.
