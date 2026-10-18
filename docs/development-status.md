# 0.6.0 backend features and change inventory

**English** | [简体中文](development-status.zh-CN.md)

Reviewed on 2026-10-18. This document records the 0.6.0 feature scope and validation boundaries. See GitHub Release for published status and image addresses.

## Feature scope

| Feature | Implementation and behavior | State |
| --- | --- | --- |
| Media deletion | Authenticated `DELETE /api/v1/items/{id}` with `deleteMode=off/trash/permanent`, defaulting to `off`. Deletion requires explicit enablement and writable photo mounts. Health/pair advertise `delete` only when enabled. | 0.6.0 |
| Trash | Move files into the owning library's `.itemory-trash/`, preserve relative directories and avoid overwriting existing files. Scans exclude trash. Restore manually by moving files back and rescanning. | 0.6.0 |
| Path and permission checks | Check configured and resolved library boundaries and source-directory writability. Reject symlinks, dangling links and non-directory components in trash paths. Read-only photo subdirectories return `read_only_mount` even when the library root is writable. | 0.6.0 |
| Live Photos and cleanup | Process the paired clip with its still; clip IDs resolve to their parent still. After filesystem operations, invalidate thumbnails, mark index rows removed and broadcast `library/changed`. Cleanup or secondary clip failures produce warnings; an already completed file deletion is not presented as untouched. A pair is not an atomic filesystem transaction. | 0.6.0 |
| Settings compatibility | Older clients omitting `deleteMode` in PUT preserve its current value. PATCH supports explicit changes. Missing/unknown stored values become `off`; older settings cannot accidentally enable deletion. | 0.6.0 |
| Scan completeness | Health and scan status expose `scan.complete`. It becomes true only after a successful full/incremental scan reconciles the index. It is false in a fresh process, while scanning, and after failure, cancellation or an incomplete scan. Clients can use it when validating whole-day media lists; no batch-delete API was added. | 0.6.0 |
| Domestic registry | All templates and the installer default to the public Tencent Cloud registry; GHCR remains an overseas source. Version 0.6.0 supports amd64/arm64. Publishing verifies config/layer digests, anonymous pulls, RootFS and executable versions before advancing stable. | Published; synchronization workflow pushed |
| Automatic installation/upgrades | Local Compose installations, read-only photo mounts for new installs, `/data` backups and preservation of mounts, accounts, pairing, settings and runtime options on upgrade. Configuration drift is rejected before stopping the service. Explicit `tcr/ghcr/oss` sources with no silent switching. | Installer published |
| Universal OSS bundles | Architecture-specific archives, SHA-256 and nine system templates pinned to a version with registry pulls disabled. | 0.6.0 offline distribution; automatic uploads require credentials and `OSS_PUBLISH_ENABLED=true` |

Deletion routes and handlers live in `internal/api/`; settings in `internal/config/`; index/cache cleanup in `internal/store/` and `internal/thumbs/`; completeness in `internal/scan/`. Distribution is implemented in `.github/workflows/`, `scripts/`, `install.sh` and `deploy/compose/`.

## Merge scope

Retain the three existing local deletion/documentation/read-only fix commits, merge the published domestic-distribution main, and include pending trash-path protection, scan completeness, tests and bilingual documentation. Release tag v0.6.0 uses the same source for both GHCR and the domestic multi-platform images.

A service release does not imply that client features have shipped. Older clients retain read compatibility. Deletion controls depend on the client version and service capabilities. Public website copy continues to describe the App Store client without advertising unreleased client deletion interactions.

## Validation and limitations

- `make web && make test && make vet` passed: dashboard static build, full Go tests and vet.
- Deletion tests cover default-off behavior, authorization, trash, outside-library paths, symlinks, read-only roots/subdirectories, paired clips, index hiding, thumbnail invalidation and settings compatibility. Scan tests cover successful, cancelled, failed and incomplete `complete` states.
- `bash -n install.sh`, 17 installer tests, `python3 -m py_compile scripts/*.py`, `gofmt -l cmd internal` and Git diff checks passed.
- Every existing merged file was compared byte-for-byte against the tested snapshot; only these inventory documents were added afterward.
- Real NAS deletion, client whole-day deletion and deployment of a newly built development image remain unverified. Earlier 0.5.0 distribution tests do not validate the new deletion implementation on devices.
- `npm ci` reported 13 findings in existing locked dependencies (2 moderate, 10 high, 1 critical). Dependencies and the lockfile were not changed; upgrades remain follow-up work.

User guides: [deletion](configuration.md#deleting-photos), [online installation](online-install.md), [Tencent Cloud publishing](tcr-distribution.md), [OSS distribution](oss-distribution.md).
