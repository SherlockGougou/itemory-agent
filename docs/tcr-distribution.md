# Tencent Cloud domestic image publishing

**English** | [简体中文](tcr-distribution.zh-CN.md)

All supported Compose templates and the installer default to the public registry:

```yaml
image: ccr.ccs.tencentyun.com/itemory/itemory-agent:1
```

End users need no login. Docker selects `linux/amd64` or `linux/arm64`. Pin a release with `:0.5.0`. Overseas users can use `ghcr.io/sherlockgougou/itemory-agent:1`. When registries are unavailable, use [universal OSS bundles](oss-distribution.md); an OSS file URL cannot be used as Compose `image:`.

## Publisher configuration

Create public namespace `itemory` and public repository `itemory-agent` in Tencent Cloud personal edition. Set GitHub Actions Secrets `TCR_USERNAME` and `TCR_PASSWORD`. Optional Variable `TCR_REPOSITORY` defaults to the address above. Credentials are for publishing only and must never appear in source, user guides or install commands.

`release.yml` calls `tcr-images.yml` after the GHCR release. To mirror an existing release, dispatch `tcr-images` with the full version. Workflow and publisher source must first be committed and pushed; setting Secrets alone does not activate local workflow changes.

Local publishing requires Python 3.9+, Docker with multi-platform image inspection, Skopeo and cross-platform execution support such as QEMU. Log in using password standard input, then run:

```bash
export TCR_REPOSITORY=ccr.ccs.tencentyun.com/itemory/itemory-agent
export REGISTRY_AUTH_FILE="$HOME/.docker/config.json"
python3 scripts/publish_tcr_images.py --version 0.5.0
```

The publisher copies the two runnable GHCR manifests as Docker v2 schema 2, publishing architecture tags and a multi-platform version tag without rebuilding. GHCR retains original provenance/SBOM. Source and destination config/layer digests must match. Empty-auth Docker pulls and version execution checks verify both architectures. Existing releases are reused only when content matches; other errors fail explicitly.

The Tencent Cloud `:1` tag advances only when the version exactly matches current GHCR `:1` and is not a prerelease. Backfilling an older release cannot downgrade stable. Evidence is written to `.agent-temp/tcr-release/tcr-verification.json` and retained as a CI artifact.

## Service limits

Personal edition is shared infrastructure without an SLA. It serves current public distribution needs but does not guarantee download speed for every carrier or region. GHCR and OSS remain independent alternatives. Changing image providers must preserve photo mounts, uid:gid, ports and `/data`. Prefer automatic upgrades for existing installations to preserve backups and pairing.

Official references: [Tencent Cloud Container Registry](https://cloud.tencent.com/document/product/1141), [Skopeo copy](https://github.com/containers/skopeo/blob/main/docs/skopeo-copy.1.md), [Docker manifest](https://docs.docker.com/reference/cli/docker/manifest/).
