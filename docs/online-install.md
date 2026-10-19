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

The runtime user is suggested from the first photo folder's owner, then checked in the actual image with read-only mounts. This checks the selected roots and a sample file; it cannot certify every nested ACL. Photos are mounted read-only, deleting remains disabled by the service's default settings, and photo ownership/permissions are never modified. Only the newly created service data directory may have its ownership set. A new installation sets no hard CPU cap (Docker checks `cpus` against the machine's core count, so no fixed value suits both single-core and many-core NAS models); CPU use is governed by the concurrency of the `balanced` preset. The memory limit is 2 GiB. To cap or speed things up, see [CPU limit](configuration.md#cpu-limit). Larger videos may require more RAM as described in [memory limit](configuration.md#memory-limit).

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

## Logs and feedback

At the start, the installer prints the log path for this run, of the form `~/.cache/itemory-agent/logs/install-<YYYYMMDD-HHMMSS>-<pid>.log`. The log directory is mode 700 and the file is mode 600. Under `sudo` the log goes to the same location in the home directory of the account that ran sudo, and the script hands the file back to that account; when you are logged in as root directly it lands in `/root/.cache/itemory-agent/logs/`. If the home directory is not writable it falls back to `/tmp/itemory-agent-logs/`; if that fails too, the installer warns and keeps going, leaving only the on-screen output. Use the path the script prints.

The log records the commands run in each stage, check results and Docker command output, so you don't need to capture the terminal separately when reporting a problem. It never records passwords, pairing tokens or photo contents, and suspected credentials in command output are replaced before writing.

- `--log-file <absolute-path>`: write the log to a location you choose. Only an absolute path is accepted. Keep it out of your photo and service-data folders.
- `--debug`: also show the more detailed steps and command output on screen. That detail is written to the log file either way; `--debug` only makes it visible too.

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --debug --log-file <absolute-path>
```

The console marks the current stage as `[n/7]` and names it, for example `[4/7]` while fetching the image. Upgrades and new installations pass through different stages, so the numbers are not necessarily consecutive. On failure the script prints the reason, the failing stage, the log path, the command that copies the last 200 lines, and the support address:

```bash
tail -n 200 <log-path>
```

When Docker returns an error, the message lists the common causes for that output, such as insufficient Docker Socket permissions, too little disk space, a port already in use, an unsupported architecture or an unavailable image source. See [troubleshooting](troubleshooting.md) for further steps.

When an installation or upgrade finishes, whether it succeeded or failed, the script saves a second copy of the log as `install.log` in the installation-record directory and prints the path of that copy. For a new installation the record directory is `<installation>/.itemory-online/<timestamp-id>/`; for an upgrade it sits next to `data.tar`. That directory is mode 700 and also holds the active configuration and sensitive backups: don't publish or delete it. A failure that happens before the record directory exists (argument checks, a taken port, Compose drift) leaves no copy, so send the default log file printed earlier instead. A run that finds the image already current and only checks health also creates no record directory.

When submitting a report, send the log copy, your NAS model and system version, and the exact command you ran to qinglingou@gmail.com, or [open an issue](https://github.com/SherlockGougou/itemory-agent/issues/new). If root created the log, reading or copying it requires `sudo`.

## Existing installations and upgrades

The script detects official Itemory containers, including containers created from the existing Compose templates. It reads the original Compose files, project name and working directory from Docker labels. Original files must still exist, and the resolved config hash must match the running container. Missing configuration, config drift, unsupported data layouts and ambiguous installations cause a failure **before the service is stopped**. If Compose itself has changed versions, reconcile the configuration and running service using the original Compose version before retrying.

Itemory containers created by older `docker run` commands or local image tags do not have Compose configuration. The installer does not treat them as an ordinary upgrade. It first shows the container name, image, `/data` and photo mounts, runtime user, ports and resource limits, then explains why it cannot take over directly and asks the user to choose:

- `1. Migrate and continue`: stop the old container, back up `/data`, rename the old container to `itemory-agent-before-migration-<timestamp>` for rollback, generate a Compose project with the detected mounts, user, environment, ports and resource limits, start the official image, and verify health, administrator credentials, `serverId` and pairings. Photos are never deleted.
- `2. Exit`: stop without changing the running service.

The migration prompt is split into detected state, current configuration, what migration does, what stays unchanged, and a numbered choice, so no English command needs to be remembered. With non-interactive execution, `--yes` alone never migrates a running service. Use `--migrate-existing` only when you explicitly accept that migration. The installer refuses automatic migration when it finds custom entrypoint/command values, a custom working directory, non-default networking, added capabilities, devices, custom DNS or other runtime settings it cannot preserve safely; see the [upgrade guide](upgrading.md) for manual migration.

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

`--yes` skips confirmation and requires explicit photo paths for a new installation. When an old-style container is detected, `--yes` still stops before any change: choose `1. Migrate and continue` interactively or pass `--migrate-existing` when migration is explicitly accepted. `--help` lists the available options. Installation and migration reject new paths, users and other deployment changes; edit the active Compose config separately if you intend to change deployment settings.

## Failure and recovery

A pull or preflight failure leaves the old service untouched. If a backup fails after stopping the old service, the script attempts to restart that same container. If replacement or validation fails, it attempts to stop the target service and prints the backup location. It reports failure and does **not** claim an automatic rollback: a newer service may already have migrated its database.

For recovery, preserve the failed data and use the stopped-service `data.tar` backup:

1. Inspect `before-container.json` and `before-compose.json` in the reported directory. The latter pins the previous local image ID. Do not prune that image.
2. Restore `data.tar` into a **new, empty directory** using `tar -xpf data.tar -C <recovery-directory>` with appropriate permissions. Do not overwrite the failed data or extract into your photo folder.
3. Copy `before-compose.json` to a recovery configuration. Change only the target service's `/data` mount to a bind mount for the recovery directory, preserving its original non-root user. Keep photo mount paths unchanged.
4. Run `docker compose --project-directory <original-directory> -p <original-project> -f <recovery-config> up -d --no-deps --no-build --pull never <service>`. Check health, logs, settings and phone access before removing any recovery material.

Do not delete the installation-record directory while the service uses a Compose file inside it. Running `docker compose down -v` can delete named volumes and is not part of this installer.
