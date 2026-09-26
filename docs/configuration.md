# Configuration

**English** | [简体中文](configuration.zh-CN.md)

Most settings are changed from the Itemory app under **Enhanced Service Settings**. The compose template only controls how the container itself runs.

- [Template options](#template-options)
- [Performance presets](#performance-presets)
- [Memory limit](#memory-limit)
- [CPU limit](#cpu-limit)
- [Settings in the app](#settings-in-the-app)
- [HTTPS and remote access](#https-and-remote-access)

## Template options

| Line | Default | What it does | Change it? |
| --- | --- | --- | --- |
| `image` | `ghcr.io/sherlockgougou/itemory-agent:1` | The image to run. `:1` always points to the latest release; use a version such as `:0.4.0` to pin one. | Only to pin a version |
| `user` | `"1000:1000"` | The user ID and group ID the agent runs as. See [installation step 3](installation.md#step-3-pick-the-run-user). | If folders are unreadable |
| `volumes` | varies | Photo folders (read-only, under `/volumes/…`) and the data folder (`/data`). | Yes, always |
| `ports` | `"8787:8787"` | `NAS port:container port`. | If 8787 is taken; change the first number |
| `TZ` | `Asia/Shanghai` | Time zone used to decide which day a photo belongs to. | Yes |
| `ITEMORY_PRESET` | `balanced` | Initial [performance preset](#performance-presets). Only used on the very first start. | Optional |
| `mem_limit` | `2g` | Maximum memory. See [Memory limit](#memory-limit). | If you have large videos |
| `cpus` | `"1.5"` | Maximum CPU cores. See [CPU limit](#cpu-limit). | Optional |
| `restart` | `unless-stopped` | Start again after a crash or a NAS reboot. | No |
| `read_only`, `tmpfs`, `cap_drop`, `security_opt` | enabled | Hardening; see below. | No |

The hardening options are safe to keep. The agent is designed to run with them:

| Option | Effect |
| --- | --- |
| `read_only: true` | The container's own file system is read-only; the agent only writes to `/data`. |
| `tmpfs: /tmp` | Scratch space for image decoding. Raise it to `256m` if you have many HEIC or RAW files. |
| `cap_drop: [ALL]` | Removes all special Linux privileges. |
| `no-new-privileges` | Processes inside cannot gain more privileges. |
| `:ro` on photo folders | The agent cannot modify or delete your photos. |

Two more environment variables exist for advanced setups. The image already sets them, so leave them alone unless you know why you need them: `ITEMORY_CACHE_DIR` (data folder, `/data`) and `ITEMORY_HTTP_ADDR` (listen address, `:8787`).

## Performance presets

A preset sets several resource limits at once. Pick the initial one with `ITEMORY_PRESET`; afterwards, change it in the app under **Enhanced Service Settings → Resource Preset**.

| Preset | Parallel jobs | Thumbnail size | Thumbnail cache | Original-file cache | Thumbnails generated per night |
| --- | --- | --- | --- | --- | --- |
| `light` | 1 | 256 px | 1 GB | 512 MB | 2,000 |
| `balanced` (default) | 2 | 512 px | 5 GB | 1 GB | 5,000 |
| `performance` | 4 | 512 px | 10 GB | 2 GB | 20,000 |

Use `light` on small ARM NAS models with little memory, and `performance` on machines with plenty of CPU and disk space.

## Memory limit

Memory is the one limit you should check against your own library. If it is too low, thumbnail or video-frame jobs are killed by the system. This happens **silently**: the only visible symptom is that some thumbnails never appear.

**What matters is your largest single video, not the number of files.** Measured on a 24-core x86 NAS: extracting a frame from one 2 GB video failed at 512 MB and succeeded at 768 MB and above. Reducing the number of parallel jobs does not help, because one large video is handled by a single process.

Choose `mem_limit` by the largest file in your library:

| Largest single file | Suggested `mem_limit` |
| --- | --- |
| up to 100 MB (photos only) | `512m` |
| up to 500 MB | `1g` |
| 1–2 GB | `2g` (template default) |
| over 2 GB | `4g` or more |

A killed job leaves no clear error message, which is why it is best to size the limit from your largest file up front. If some thumbnails stay missing, raise the limit and look again. Don't rely on the container's reported peak memory: Linux counts file cache as container memory, so that number does not reflect what the agent really needs.

## CPU limit

`cpus` only affects speed, never correctness. The template uses 1.5 cores so that the first full scan doesn't slow down other services on a small NAS. On a fast machine the same batch of thumbnails can take about 16 times longer at 1.5 cores than without a limit. If the first scan is too slow, raise `cpus` temporarily and lower it again once the library is indexed.

## Settings in the app

These are changed in the Itemory app under **Enhanced Service Settings** and take effect immediately:

- **Libraries**: which folders are indexed. Saving rebuilds the index for the new scope.
- **Folders to skip**: folder and file names that are never indexed. System folders such as `@eaDir`, `#recycle`, `#snapshot`, `.Trash` and `$RECYCLE.BIN` are skipped by default.
- **Resource Preset**, and under **Thumbnails & Cache** the thumbnail size and the cache limit on the NAS.
- **Nightly automatic scan** (03:00 by default, in the container's time zone).
- **Log verbosity**, plus **Scan for new items** and **Rescan everything**.

The web console shows status, logs and diagnostics, and manages pairing. Libraries can only be added from the app.

## HTTPS and remote access

The agent speaks plain HTTP on port 8787. On a trusted home network that is fine. Keep in mind:

- **The administrator password travels unencrypted** when you sign in over plain HTTP. If other people or devices on your network aren't trusted, put the console behind your NAS's built-in reverse proxy with HTTPS. The agent understands the standard `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-Port` headers, so a QR code generated through the proxy contains the proxy's HTTPS address; the iPhone must then trust that certificate.
- **Do not forward port 8787 from your router to the internet.** The agent is built for use inside your home network.
- To use it away from home, connect the iPhone to your home network through a VPN you control. The app then reaches the NAS as if it were at home.
