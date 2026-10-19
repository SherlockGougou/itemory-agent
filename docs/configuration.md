# Configuration

**English** | [简体中文](configuration.zh-CN.md)

Most settings are changed from the Itemory app under **Itemory Private Cloud Settings**. The compose template only controls how the container itself runs.

- [Template options](#template-options)
- [Performance presets](#performance-presets)
- [Background thumbnails](#background-thumbnails)
- [Memory limit](#memory-limit)
- [CPU limit](#cpu-limit)
- [Deleting photos](#deleting-photos)
- [Settings in the app](#settings-in-the-app)
- [HTTPS and remote access](#https-and-remote-access)

## Template options

| Line | Default | What it does | Change it? |
| --- | --- | --- | --- |
| `image` | `ccr.ccs.tencentyun.com/itemory/itemory-agent:1` | The image to run. `:1` always points to the latest release; use a version such as `:0.5.0` to pin one. | Only to pin a version |
| `user` | `"1000:1000"` | The user ID and group ID the agent runs as. See [installation step 3](installation.md#step-3-pick-the-run-user). | If folders are unreadable |
| `volumes` | varies | Photo folders (read-only, under `/volumes/…`) and the data folder (`/data`). See [Deleting photos](#deleting-photos) before removing `:ro`. | Yes, always |
| `ports` | `"8787:8787"` | `NAS port:container port`. | If 8787 is taken; change the first number |
| `TZ` | `Asia/Shanghai` | Time zone used to decide which day a photo belongs to. | Yes |
| `ITEMORY_PRESET` | `balanced` | Initial [performance preset](#performance-presets). Only used on the very first start. | Optional |
| `mem_limit` | `2g` | Maximum memory. See [Memory limit](#memory-limit). | If you have large videos |
| `cpus` | not set | Docker's hard CPU cap. See [CPU limit](#cpu-limit). It is unset by default, so usage is governed by the parallel jobs of the [performance preset](#performance-presets); if you add the line yourself, the value can't exceed the NAS's CPU cores. | Optional; only to cap CPU use |
| `restart` | `unless-stopped` | Start again after a crash or a NAS reboot. | No |
| `read_only`, `tmpfs`, `cap_drop`, `security_opt` | enabled | Hardening; see below. | No |

The hardening options are safe to keep. The agent is designed to run with them:

| Option | Effect |
| --- | --- |
| `read_only: true` | The container's own file system is read-only; the agent only writes to `/data`. |
| `tmpfs: /tmp` | Scratch space for image decoding. Raise it to `256m` if you have many HEIC or RAW files. |
| `cap_drop: [ALL]` | Removes all special Linux privileges. |
| `no-new-privileges` | Processes inside cannot gain more privileges. |
| `:ro` on photo folders | The agent cannot write to, move or delete your photos. This is the default and the main protection; see [Deleting photos](#deleting-photos). |

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

`cpus` is a hard limit enforced by Docker, and Docker checks it against the number of CPU cores in the machine: a value above that number makes container creation fail with `Range of CPUs is from 0.01 to N.00, as there are only N CPUs available`. The templates and the online installer therefore no longer set it. No single fixed value suits both a single-core and a many-core machine: anything above 1 breaks a single-core NAS, while 1 throttles a larger machine.

With no `cpus` set there is no hard CPU ceiling. How much CPU the agent uses is decided by the parallel jobs of the [performance preset](#performance-presets): `light` processes 1 file at a time, `balanced` (the default) 2, and `performance` 4. To use less CPU, pick `light` under **Resource Preset** in the app or the web console, or lower **Concurrent tasks** on its own.

`cpus` only affects speed, never correctness: thumbnails and the index are still built, just more slowly. A cap makes a full-library thumbnail batch noticeably slower; on a fast machine, capping it at 1.5 cores made the same batch take about 16 times longer than with no limit. Add it yourself only when you really need to cap CPU use. The syntax is `cpus: "2"` in the template (quoted), and the number has to follow these rules:

- it can't exceed the CPU cores of this machine, or Docker refuses to create the container;
- on a single-core machine the only valid value is `"1"`.

To speed up the first scan, `cpus` is not the knob to turn: switch the [performance preset](#performance-presets) to `performance` under **Itemory Private Cloud Settings → Resource Preset** in the app (or **Resource Preset** in the web console) and switch back afterwards. Before the very first start, a new installation can also set `ITEMORY_PRESET` to `performance` in the template.

## Deleting photos

Deleting is **off by default**. The agent never removes a file unless you turn it on deliberately, and two independent things have to be true at the same time:

1. **The photo folder must be mounted writable.** Every template mounts photo folders with `:ro` (read-only). Drop that suffix from the volume line, for example `/volume1:/volumes/volume1` instead of `/volume1:/volumes/volume1:ro`, and recreate the container.
2. **`deleteMode` must be `trash` or `permanent`.** It lives in the agent's settings (`/data/settings.json`, key `deleteMode`) and defaults to `off`. Set it through the settings API, for example:

```bash
curl -X PATCH http://<your-NAS-IP>:8787/api/v1/settings \
  -H "Authorization: Bearer <device token>" \
  -H "Content-Type: application/json" \
  -d '{"deleteMode":"trash"}'
```

If either one is missing, deleting is refused: the folder stays read-only, or the request fails with `403 delete_disabled`. The app only shows a delete entry point when the running service reports the `delete` capability, which it does only while `deleteMode` is not `off`. Deleting is all-or-nothing per file: either the file is moved or removed, or nothing changes and you get an error naming the path.

| `deleteMode` | What a delete does |
| --- | --- |
| `off` (default) | Deleting is refused with `403 delete_disabled`. The agent only reads. |
| `trash` | The file is moved into `<library root>/.itemory-trash/`, keeping its subfolders, with a timestamp prefix on the file name so an older file is never overwritten. |
| `permanent` | The file is deleted from disk. This cannot be undone. |

With `trash`, the original path is gone from the library immediately, and the file waits under the library root:

```text
/volume1/photos/2024/IMG_0001.jpg   →   /volume1/photos/.itemory-trash/2024/20260117T101500.123456789_IMG_0001.jpg
```

`.itemory-trash` starts with a dot, so scans skip it and the moved files never show up in the app again. To restore a file, move it back out of `.itemory-trash` to where it belongs and run a scan; to reclaim the space, delete the `.itemory-trash` folder yourself.

Deleting is refused with these errors:

| Response | Meaning |
| --- | --- |
| `403 delete_disabled` | `deleteMode` is `off`. |
| `403 read_only_mount` | The directory that has to be written — the file's own folder or the trash folder — is not writable, typically because the photo folder is mounted read-only (`:ro`). Drop `:ro` and recreate the container. |
| `500 delete_failed` | The write failed for another reason, for example the file is locked or on a filesystem that refuses the operation. The error message names the path. The file is left in place. |
| `403 outside_library` | The indexed path is no longer inside a configured library, or the trash directory contains a symbolic link or a non-directory component. |
| `404 not_found` | No live entry with that id. |

> [!IMPORTANT]
> Deleting is the only operation in which the agent writes inside your photo folders, and it only works when both switches are on. Turning `deleteMode` back to `off` and restoring `:ro` returns the agent to read-only behaviour; it does not undo files that were already deleted.

The trash folder and its subfolders must be real directories inside the library, not symbolic links. This includes links to another directory inside the same library and dangling links. Such paths are rejected before the writable-folder probe or any media move, leaving the original file and its index entry unchanged.

For deleting a whole day, the app requires a complete day list. The health response exposes `scan.complete`: it is true only after a successful full or incremental scan has reconciled the index, and false during a scan, after failure or cancellation, or before a scan has completed in the current server process. Clients also compare the day list with its independent item count. Older servers without this field still support individual deletion; update the service and complete a scan before using day deletion. On a partial deletion failure, retry only the failed originals; originals already removed are not deleted again.

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

`deleteMode` is not in this list yet: neither the console nor the app has a control for it in this release. Set it with the `PATCH` request shown under [Deleting photos](#deleting-photos), and leave it `off` if you are not sure.

## HTTPS and remote access

The agent speaks plain HTTP on port 8787. On a trusted home network that is fine. Keep in mind:

- **The administrator password travels unencrypted** when you sign in over plain HTTP. If other people or devices on your network aren't trusted, put the console behind your NAS's built-in reverse proxy with HTTPS. The agent understands the standard `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-Port` headers, so a QR code generated through the proxy contains the proxy's HTTPS address; the iPhone must then trust that certificate.
- **Do not forward port 8787 from your router to the internet.** The agent is built for use inside your home network.
- To use it away from home, connect the iPhone to your home network through a VPN you control. The app then reaches the NAS as if it were at home.
