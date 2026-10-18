# Backend features and change inventory

**English** | [简体中文](development-status.zh-CN.md)

Reviewed on 2026-10-18. Published distribution and local development features have different release states. A code merge is not a service release.

## Feature scope

| Feature | Implementation and behavior | State |
| --- | --- | --- |
| Media deletion | Authenticated `DELETE /api/v1/items/{id}` with `deleteMode=off/trash/permanent`, defaulting to `off`. Deletion requires explicit enablement and writable photo mounts. Health/pair advertise `delete` only when enabled. | Local development; no released image |
| Trash | Move files into the owning library's `.itemory-trash/`, preserve relative directories and avoid overwriting existing files. Scans exclude trash. Restore manually by moving files back and rescanning. | Local development |
| Path and permission checks | Check configured and resolved library boundaries and source-directory writability. Reject symlinks, dangling links and non-directory components in trash paths. Read-only photo subdirectories return `read_only_mount` even when the library root is writable. | Local development |
| Live Photos and cleanup | Process the paired clip with its still; clip IDs resolve to their parent still. After filesystem operations, invalidate thumbnails, mark index rows removed and broadcast `library/changed`. Cleanup or secondary clip failures produce warnings; an already completed file deletion is not presented as untouched. A pair is not an atomic filesystem transaction. | Local development |
| Settings compatibility | Older clients omitting `deleteMode` in PUT preserve its current value. PATCH supports explicit changes. Missing/unknown stored values become `off`; older settings cannot accidentally enable deletion. | Local development |
| Scan completeness | Health and scan status expose `scan.complete`. It becomes true only after a successful full/incremental scan reconciles the index. It is false in a fresh process, while scanning, and after failure, cancellation or an incomplete scan. Clients can use it when validating whole-day media lists; no batch-delete API was added. | Local development |
| Domestic registry | All templates and the installer default to the public Tencent Cloud registry; GHCR remains an overseas source. Published 0.5.0 supports amd64/arm64. Publishing verifies config/layer digests, anonymous pulls, RootFS and executable versions before advancing stable. | Published; synchronization workflow pushed |
| Automatic installation/upgrades | Local Compose installations, read-only photo mounts for new installs, `/data` backups and preservation of mounts, accounts, pairing, settings and runtime options on upgrade. Configuration drift is rejected before stopping the service. Explicit `tcr/ghcr/oss` sources with no silent switching. | Installer published |
| Universal OSS bundles | Architecture-specific archives, SHA-256 and nine system templates pinned to a version with registry pulls disabled. Legacy UGREEN ZIP links remain. | 0.5.0 downloads published; automatic uploads require credentials and `OSS_PUBLISH_ENABLED=true` |

Deletion routes and handlers live in `internal/api/`; settings in `internal/config/`; index/cache cleanup in `internal/store/` and `internal/thumbs/`; completeness in `internal/scan/`. Distribution is implemented in `.github/workflows/`, `scripts/`, `install.sh` and `deploy/compose/`.

## Merge scope

Retain the three existing local deletion/documentation/read-only fix commits, merge the published domestic-distribution main, and include pending trash-path protection, scan completeness, tests and bilingual documentation. Preserve the remote publishing and installer implementation. Do not republish 0.5.0 or create a new tag.

This is a local backend merge commit. New deletion features are not pushed, the parent repository's submodule pointer is not updated, and public website copy continues to describe released service behavior. A service release must synchronize client support, website facts and release records.

## Validation and limitations

- `make web && make test && make vet` passed: dashboard static build, full Go tests and vet.
- Deletion tests cover default-off behavior, authorization, trash, outside-library paths, symlinks, read-only roots/subdirectories, paired clips, index hiding, thumbnail invalidation and settings compatibility. Scan tests cover successful, cancelled, failed and incomplete `complete` states.
- `bash -n install.sh`, 17 installer tests, `python3 -m py_compile scripts/*.py`, `gofmt -l cmd internal` and Git diff checks passed.
- Every existing merged file was compared byte-for-byte against the tested snapshot; only these inventory documents were added afterward.
- Real NAS deletion, client whole-day deletion and deployment of a newly built development image remain unverified. Earlier 0.5.0 distribution tests do not validate the new deletion implementation on devices.
- `npm ci` reported 13 findings in existing locked dependencies (2 moderate, 10 high, 1 critical). Dependencies and the lockfile were not changed; upgrades remain follow-up work.

User guides: [deletion](configuration.md#deleting-photos), [online installation](online-install.md), [Tencent Cloud publishing](tcr-distribution.md), [OSS distribution](oss-distribution.md).
