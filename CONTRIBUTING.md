# Contributing

**English** | [简体中文](CONTRIBUTING.zh-CN.md)

Bug reports, fixes and improvements are welcome. For larger changes, please open an issue first so we can agree on the approach.

## Requirements

- Go 1.24 or later
- Node.js 22 (for the web console)
- Optional: Docker, to build the image; `libvips` (`vipsthumbnail`) and `ffmpeg` to generate thumbnails locally. On macOS the agent falls back to the built-in `sips` for photos.

## Build and run

```bash
make web      # build the web console; required before any Go build
make test     # go test ./...
make vet      # go vet ./...
make build    # binary at dist/itemory-agent
make run      # run locally on :8787 with data in ./data
```

The web console is a Next.js static export in `internal/api/web`. Its output (`internal/api/web/out`) is embedded into the binary with `//go:embed` and is not committed, so a fresh checkout won't compile until `make web` has run. `npm run dev` serves the console without the API, so to see console changes with real data, use `make run`.

To check the Docker build from a clean context:

```bash
make web-check
```

## Project layout

| Path | Purpose |
| --- | --- |
| `cmd/itemory-agent` | Entry point: `serve`, `healthcheck`, `benchmark`, `version` |
| `internal/scan` | Incremental scanning based on modification time and size |
| `internal/media` | EXIF, RAW previews, Live Photo pairing, video probing (reads file headers and trailers only) |
| `internal/store` | SQLite (WAL) index |
| `internal/thumbs` | Thumbnail generation (`vipsthumbnail` → `sips` → `ffmpeg` → pure Go) and cache eviction |
| `internal/api` | `/api/v1` REST and SSE, administrator sessions, device pairing |
| `internal/api/web` | Web console (Next.js) |
| `internal/config` | `settings.json`, presets and validation |
| `deploy/compose` | Compose templates for each NAS |
| `docs` | User documentation |

## Guidelines

- **Compatibility**: the Itemory app depends on `/api/v1`. Add fields and endpoints instead of changing or removing existing ones.
- **Formatting**: run `gofmt`; CI rejects unformatted Go code.
- **Tests**: put Go tests next to the code as `*_test.go`, and cover the behaviour you change.
- **Console text**: every string in `internal/api/web/src/lib/i18n.json` exists in five languages (`zh-Hans`, `zh-Hant`, `en`, `ja`, `ko`). Add all five when you add a string.
- **Documentation**: user documentation is written in English, with a Simplified Chinese version next to it (`*.zh-CN.md`). Update both when behaviour changes.
- **Commits**: use [Conventional Commits](https://www.conventionalcommits.org/), for example `fix(thumbs): …` or `docs: …`.

## Releasing

Maintainers publish a release by pushing a version tag:

```bash
git tag -a v0.4.1 -m "v0.4.1"
git push origin v0.4.1
```

`.github/workflows/release.yml` then runs the tests, builds the `linux/amd64` and `linux/arm64` image, pushes `ghcr.io/sherlockgougou/itemory-agent:<version>` and `:1`, and smoke-tests both architectures.

## License

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
