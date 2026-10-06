# Configuration

**English** | [简体中文](configuration.zh-CN.md)

Most settings are changed from the Itemory app under **Itemory Private Cloud Settings**. The compose template only controls how the container itself runs.

- [Template options](#template-options)
- [Performance presets](#performance-presets)
- [Background thumbnails](#background-thumbnails)
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
| `cpus` | `"1.5"` | Maximum CPU cores. See [CPU limit](#cpu-limit). | Optional; must be lowered on a single-core machine |
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

A preset sets several resource limits at once. Pick the initial one with `ITEMORY_PRESET`; afterwards, change it in the app under **Itemory Private Cloud Settings → Resource Preset**.

| Preset | Parallel jobs | Thumbnail size | Thumbnail cache | Nightly thumbnail limit |
| --- | --- | --- | --- | --- |
| `light` | 1 | 256 px | 1 GB | 2,000 |
| `balanced` (default) | 2 | 512 px | 5 GB | 5,000 |
| `performance` | 4 | 512 px | 10 GB | 20,000 |

Use `light` on small ARM NAS models with little memory, and `performance` on machines with plenty of CPU and disk space. After choosing a preset you can still change each value on its own in the app.

## Background thumbnails

A thumbnail is a small JPEG copy of a photo, or one frame of a video, stored in the data folder under `thumbs/`. The app shows thumbnails while you scroll and only loads the original when you open a photo.

The agent makes thumbnails in two ways:

- **Ahead of time.** Each time a scan finishes (the nightly scan, the first scan after you add a folder, or one you start by hand), the agent goes through the library from the newest photo to the oldest and makes the thumbnails that are still missing.
- **On request.** If the app asks for a thumbnail that doesn't exist yet, the agent makes it right then. The photo shows up a little later the first time and instantly afterwards.

Three settings in the app limit the work done ahead of time:

| Setting in the app | Effect |
| --- | --- |
| **Nightly thumbnail limit** | The most thumbnails made after one scan. A large library is therefore covered over several nights instead of keeping the NAS busy until morning. `0` turns generation ahead of time off; thumbnails are then only made on request. |
| **Concurrent tasks** | How many files are processed at the same time. |
| **Private cloud cache limit** | The size of the thumbnail cache. Generation ahead of time stops when the cache is 90% full. When the cache is full, the thumbnails that haven't been used for the longest time are deleted first. |

Roughly, a 512 px thumbnail takes 40–80 KB, so a 5 GB cache holds on the order of 80,000 thumbnails. If your library is larger than that, raise the cache limit, or accept that the oldest photos get their thumbnails on request.

**Clear Cache** in the app deletes all thumbnails and stops generation that is in progress. Nothing else is lost; thumbnails come back on request and after the next scan.

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

`cpus` only affects speed, never correctness. It can't be higher than the number of CPU cores in the machine: on a single-core NAS, Docker refuses to create the container until you change it to `"1"` or lower. The template uses 1.5 cores so that the first full scan doesn't slow down other services on a small NAS. On a fast machine the same batch of thumbnails can take about 16 times longer at 1.5 cores than without a limit. If the first scan is too slow, raise `cpus` temporarily and lower it again once the library is indexed.

## Settings in the app

These are changed in the Itemory app under **Itemory Private Cloud Settings** and take effect immediately:

| Setting | What it does |
| --- | --- |
| **Libraries** | Which folders are indexed. Saving starts a scan right away: new folders are added, and removed folders disappear from the app. |
| **Folders to skip** | Folder and file names that are never indexed. System folders such as `@eaDir`, `#recycle`, `#snapshot`, `.Trash` and `$RECYCLE.BIN` are skipped by default. |
| **Resource Preset** | Switches all the values of a [performance preset](#performance-presets) at once. |
| **Thumbnail Quality** | The thumbnail size in pixels. Larger looks sharper on big screens and takes more cache space. Existing thumbnails of the old size stay in the cache until they are pushed out. |
| **Private cloud cache limit** | The maximum size of the thumbnail cache. Lowering it frees the space immediately. |
| **Nightly Automatic Scan**, **Scan Time** | Whether and when the daily scan runs (03:00 by default, in the container's time zone). |
| **Concurrent tasks** | How many files are processed in parallel during scans and thumbnail generation (1–8). |
| **Nightly thumbnail limit** | See [Background thumbnails](#background-thumbnails). |
| **Scan for new items** | Looks for new, changed and deleted files now. Unchanged files are not read again. |
| **Rescan everything** | Reads every file again. Only needed after changing `TZ`, or when asked to in [Troubleshooting](troubleshooting.md). |
| **Live Photo detection** | Pairs a photo with the video of the same name in the same folder and plays them as a Live Photo. |
| **Log verbosity**, **Server Log** | How much the agent logs, and the most recent log lines. |

**Video Transcoding** is reserved for a later version and currently has no effect: the agent sends videos to the app exactly as they are stored.

The web console shows status, logs and diagnostics, manages pairing and paired devices, and can change the preset, thumbnail size, cache limit and scan time. Libraries and folders to skip can only be changed from the app.

## HTTPS and remote access

The agent speaks plain HTTP on port 8787. On a trusted home network that is fine. Keep in mind:

- **The administrator password travels unencrypted** when you sign in over plain HTTP. If other people or devices on your network aren't trusted, put the console behind your NAS's built-in reverse proxy with HTTPS. The agent understands the standard `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-Port` headers, so a QR code generated through the proxy contains the proxy's HTTPS address; the iPhone must then trust that certificate.
- **Do not forward port 8787 from your router to the internet.** The agent is built for use inside your home network.
- To use it away from home, connect the iPhone to your home network through a VPN you control. The app then reaches the NAS as if it were at home.
