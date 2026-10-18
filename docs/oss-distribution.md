# OSS image distribution and universal offline installation

**English** | [简体中文](oss-distribution.zh-CN.md)

Online installs default to the [public Tencent Cloud registry](tcr-distribution.md); OSS is the universal offline alternative. OSS hosts Docker image archive files, not a Registry endpoint. Do not advertise downloads as available before configuring upload credentials, anonymous GET access for the release prefix, and verifying public downloads.

## Publisher setup

Configure GitHub Actions Variables `OSS_BUCKET=itemory-downloads`, `OSS_REGION=cn-chengdu`, `OSS_PREFIX=releases/itemory-agent`, and `OSS_DOWNLOAD_BASE=https://itemory-downloads.oss-cn-chengdu.aliyuncs.com/releases/itemory-agent`. Configure Secrets `OSS_ACCESS_KEY_ID` and `OSS_ACCESS_KEY_SECRET`; temporary credentials additionally need `OSS_SESSION_TOKEN`. The upload principal only needs `oss:PutObject` on this prefix, not access to other objects or Bucket ACL changes. CI installs official ossutil 2.4.0 after checking its published checksum.

Use a dedicated download Bucket. Keep its ACL private, disable Block Public Access only for that dedicated Bucket, and merge a narrowly scoped policy allowing anonymous `oss:GetObject` for `acs:oss:*:*:itemory-downloads/releases/itemory-agent/*`. Do not weaken account-level protection or change any Bucket holding user data. Do not replace unrelated statements or enable public writes/listing. Confirm public-access changes separately. Use Standard storage; public downloads incur request and egress costs. End users never need OSS credentials. Expiring signed URLs are not stable release endpoints.

## Release process

After configuring upload credentials, set Actions Variable `OSS_PUBLISH_ENABLED=true` to enable OSS in release automation. Until enabled, Tencent Cloud synchronization runs independently and manual OSS uploads remain available.

`release.yml` calls `oss-images.yml` after GHCR build and smoke checks. To distribute an existing release without rebuilding it, run `oss-images` manually with the complete version (for example `0.5.0`). The publisher requires Python 3.9+, Docker API 1.49+ and QEMU for cross-platform version checks. To package locally without uploading:

```bash
python3 -m pip install -r scripts/requirements-publish.txt
python3 scripts/publish_oss_images.py --version 0.5.0 --output .agent-temp/oss-0.5.0 --package-only
```

Each version contains amd64/arm64 `.tar.gz` archives, checksums, universal `-offline.zip` bundles and a manifest. Universal ZIPs include the archive, SHA256SUMS, all nine pinned templates under `compose/`, and bilingual instructions. Templates prohibit registry pulls. Images come from published GHCR releases without rebuilding and are checked for platform, source label and binary version.

Versioned objects under `versions/<version>/` cannot be overwritten; retries accept matching size and SHA-256 metadata, or verify the full download when checksum metadata is missing. Both image archives must pass complete anonymous HTTPS download verification before updating `channels/1.json` and the stable `channels/1/itemory-agent-<arch>-offline.zip` links/checksums. Official 0.x releases retain the existing stable channel `1`; prereleases do not update stable channels. Failures retain uploaded objects and do not advance the installer channel.

## Offline installation on all systems

1. Check the Docker server architecture: Intel/AMD or x86_64 means amd64; 64-bit ARM or aarch64 means arm64.
2. Download the matching universal ZIP from the website and extract it. Import its image using the NAS local-image UI or `docker image load -i itemory-agent-<version>-<arch>.tar.gz`. If the GUI requires `.tar`, decompress gzip first. Do not use `docker import`.
3. Select `compose/ugreen.yml`, `synology.yml`, `fnos.yml`, `qnap.yml`, `unraid.yml`, `truenas.yml`, `omv.yml`, `zspace.yml` or `generic.yml`. Check actual photo/data paths, uid:gid and ports. Keep the pinned version and `pull_policy: never`. Create a project following the system guide, or run `docker compose -f compose/<system>.yml up -d --pull never`.
4. Open `http://<NAS-IP>:8787`, create an administrator and pair Itemory. New photo mounts are read-only. Privileged mode is unnecessary.

For existing installations, prefer the [automatic upgrade](online-install.md), which backs up `/data` and preserves configuration and pairing. Do not overwrite existing configuration with the fresh-install template.

## Validation

```bash
bash -n install.sh
python3 scripts/test_online_installer.py
python3 scripts/test_oss_installer_docker.py --packages .agent-temp/oss-0.5.0
```

The integration test downloads real archives over trusted local HTTPS and exercises installation, repeated runs, legacy GHCR upgrades, pairing preservation and configuration-drift rejection. It does not verify real OSS access policy, mainland download speed or the UGOS GUI; those require public OSS checks and device acceptance.

References: [ossutil](https://help.aliyun.com/zh/oss/developer-reference/ossutil-overview/), [Docker save](https://docs.docker.com/reference/cli/docker/image/save/), [Docker load](https://docs.docker.com/reference/cli/docker/image/load/), [UGREEN image management](https://support.ugnas.com/detail/article/en-US/290).
