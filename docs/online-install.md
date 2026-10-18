# Online installation and upgrades

**English** | [简体中文](online-install.zh-CN.md)

Run this command in an SSH terminal **on your NAS**. Run the same command again to upgrade:

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash
```

The script's source of truth is [`install.sh`](../install.sh); the website serves an identical copy.

## Requirements and first installation

- Bash, Python 3.9+, curl, and Docker with the Compose V2 plugin (`docker compose`). Install missing tools using your NAS's supported package manager/application center. The script does not install system dependencies.
- A local Unix Socket Docker connection, on an amd64 or arm64 Linux Docker server. Remote Docker contexts and 32-bit ARM are rejected.
- An account that can access Docker, create the installation directory, and acquire the lock beside the Docker Socket. If permissions require it, use `curl -fsSL https://itemory.gouqinglin.com/install.sh | sudo bash`; sudo reads its password from your terminal.

The script asks for one or more photo directories, an **independent, empty** installation directory, a non-root `uid:gid`, a port (default 8787), and a timezone. Prompts read `/dev/tty`, so they work with a curl pipeline. Photos, installation data and backups must not contain one another. Do not select Docker's internal data directory.

The runtime user is suggested from the first photo folder's owner, then checked in the actual image with read-only mounts. This checks the selected roots and a sample file; it cannot certify every nested ACL. Photos are mounted read-only, deleting remains disabled by the service's default settings, and photo ownership/permissions are never modified. Only the newly created service data directory may have its ownership set. The default resource limits are 1.5 CPUs and 2 GiB RAM; larger videos may require more RAM as described in [configuration](configuration.md#memory-limit).

By default, the installer pulls `ccr.ccs.tencentyun.com/itemory/itemory-agent:1` for the Docker server architecture and pins that repository’s immutable RepoDigest. No user login is required. This applies to every supported NAS and Linux system. Use `--source ghcr` overseas or explicitly select `--source oss` when a registry is unavailable. OSS downloads are checked for size and SHA-256, loaded, validated for platform and official source label, and pinned to a local Image ID. Source failures stop explicitly without silently switching providers. See [Tencent Cloud publishing](tcr-distribution.md) and [universal OSS installation](oss-distribution.md).

Compose JSON is written under `<installation>/.itemory-online/<timestamp-id>/compose.json`. Starting the service never pulls from a registry. It checks the running service's health. Open the printed address, create an administrator account, then pair from Itemory and add media folders. Container photo paths are `/volumes/photos1`, `/volumes/photos2`, etc. Installation does not create credentials or open firewall/router ports. Keep access restricted to your trusted network.

Only `--source oss` uses the archive cache, stored under the executing account's `~/.cache/itemory-agent/downloads/`. Change this with `--download-dir <absolute-path>`; keep it outside photo and service-data folders, with enough space for both download and Docker import. Cached archives are rechecked. Nothing automatically deletes caches or Docker images. Downloads have connection, low-speed and overall time limits; failure stops explicitly rather than silently falling back to GHCR.

To use the original registry path:

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --source ghcr
```

Universal offline bundles can also be imported manually; see [OSS installation](oss-distribution.md). Example:

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --source oss
```

`--download-base https://<host>/<release-prefix>` selects another compatible HTTPS distribution directory without query parameters or credentials. Checksums verify integrity; HTTPS and the release account remain the trust boundary.

To review before execution, download the script into a directory you control, inspect it, then run `bash install.sh` instead.

## Existing installations and upgrades

The script detects official Itemory containers, including containers created from the existing Compose templates. It reads the original Compose files, project name and working directory from Docker labels. Original files must still exist, and the resolved config hash must match the running container. Missing configuration, config drift, unsupported data layouts and ambiguous installations cause a failure **before the service is stopped**. If Compose itself has changed versions, reconcile the configuration and running service using the original Compose version before retrying. Existing `docker run` containers require manual migration; the script does not guess their configuration.

For an upgrade, the script:

1. Pulls the target image (or downloads, verifies and loads it when using OSS) before stopping the existing service.
2. Confirms the operation, stops only the target container, and archives the complete `/data`, including SQLite WAL files, settings, administrator credentials, pairing identity and thumbnails.
3. Saves the resolved Compose config and container metadata in the private installation-record directory (directory mode 700, files 600). Preserve this directory: it includes the active Compose configuration as well as sensitive backups. It never deletes previous backups or images.
4. Recreates only the target Compose service with the immutable image reference, keeping the same project, data volume, photo paths, runtime user, environment, port and resource settings. Existing writable photo mounts remain as previously configured; read-only is the default for **new** installations.
5. Checks health, the unchanged `/data` source, administrator credentials, `serverId`, paired-device token hashes and existing settings. A restart ends administrator browser sessions; sign in again with the same account. Phone pairing persists.

If the image is already current, it checks health without recreating the container. A stopped current container can be started after confirmation. With multiple installations, select a container:

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --container itemory-agent
```

For a specific release:

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --version 0.5.0
```

`--yes` skips confirmation and requires explicit photo paths for a new installation. `--help` lists the available options. Installation parameters such as new paths or users are rejected during upgrades; edit the active Compose config separately if you intend to change deployment settings.

## Failure and recovery

A pull or preflight failure leaves the old service untouched. If a backup fails after stopping the old service, the script attempts to restart that same container. If replacement or validation fails, it attempts to stop the target service and prints the backup location. It reports failure and does **not** claim an automatic rollback: a newer service may already have migrated its database.

For recovery, preserve the failed data and use the stopped-service `data.tar` backup:

1. Inspect `before-container.json` and `before-compose.json` in the reported directory. The latter pins the previous local image ID. Do not prune that image.
2. Restore `data.tar` into a **new, empty directory** using `tar -xpf data.tar -C <recovery-directory>` with appropriate permissions. Do not overwrite the failed data or extract into your photo folder.
3. Copy `before-compose.json` to a recovery configuration. Change only the target service's `/data` mount to a bind mount for the recovery directory, preserving its original non-root user. Keep photo mount paths unchanged.
4. Run `docker compose --project-directory <original-directory> -p <original-project> -f <recovery-config> up -d --no-deps --no-build --pull never <service>`. Check health, logs, settings and phone access before removing any recovery material.

Do not delete the installation-record directory while the service uses a Compose file inside it. Running `docker compose down -v` can delete named volumes and is not part of this installer.
