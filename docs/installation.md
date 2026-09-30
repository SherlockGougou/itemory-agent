# Installation guide

**English** | [简体中文](installation.zh-CN.md)

This guide assumes you have never run a container on your NAS before. If you have, the [Quick start](../README.md#quick-start) in the README is enough.

> [!TIP]
> **Have an AI agent?** An agent that can run commands on your NAS can do steps 1 to 4 for you. **[→ Get the AI deployment prompt](deploy-with-ai-agent.md)**, then continue here from [step 5](#step-5-create-the-administrator-account).

- [Before you start](#before-you-start)
- [Step 1: Find your photo folder](#step-1-find-your-photo-folder)
- [Step 2: Pick a data folder](#step-2-pick-a-data-folder)
- [Step 3: Pick the run user](#step-3-pick-the-run-user)
- [Step 4: Create the container](#step-4-create-the-container)
- [Step 5: Create the administrator account](#step-5-create-the-administrator-account)
- [Step 6: Pair your iPhone](#step-6-pair-your-iphone)
- [Step 7: Choose the folders to index](#step-7-choose-the-folders-to-index)
- [Step 8: Check that everything works](#step-8-check-that-everything-works)

## Before you start

Make sure you have:

- [ ] A NAS or computer that supports **Docker Compose** (see the list in [Step 4](#step-4-create-the-container)). Container support can depend on the model, so check your vendor's compatibility list if you are unsure.
- [ ] Administrator access to that NAS.
- [ ] The NAS's IP address on your home network, for example `192.168.1.20`. You can usually find it in the NAS settings or in your router's list of connected devices.
- [ ] The Itemory app on your iPhone, connected to the same Wi-Fi as the NAS.

## Step 1: Find your photo folder

The container cannot see your NAS's files unless you **mount** a folder into it. You need the folder's full path as the NAS itself sees it. Typical locations:

| NAS | Where shared folders usually live | Example |
| --- | --- | --- |
| Synology DSM | `/volume1/<shared folder>` (`/volume2/…` for a second volume) | `/volume1/photo` |
| QNAP QTS | `/share/CACHEDEV1_DATA/<shared folder>` | `/share/CACHEDEV1_DATA/Multimedia` |
| TrueNAS SCALE | `/mnt/<pool>/<dataset>` | `/mnt/tank/photos` |
| Unraid | `/mnt/user/<share>` | `/mnt/user/Photos` |
| fnOS | `/vol1/1000/<folder>` | `/vol1/1000/Photos` |
| OpenMediaVault | `/srv/dev-disk-by-uuid-<id>/<folder>` | shown under *Storage → Shared Folders* |
| Linux / other | wherever the files are | `/home/me/Pictures` |

You have two options:

- **Mount the whole volume** (what most templates do, for example `/volume1`). Later you pick the exact folders in the app. This is the easiest if your photos are spread across several folders.
- **Mount only your photo folder** (for example `/volume1/photo`). The agent then can't see anything else on the NAS.

Either way the mount is **read-only**: the agent can never change or delete your photos.

## Step 2: Pick a data folder

The agent needs one writable folder for its own files: the photo index, settings, the administrator account, the list of paired devices and the thumbnail cache. With the default settings, plan for about 6 GB of free space (thumbnail cache up to 5 GB, original-file cache up to 1 GB, plus the index).

Each template already suggests a location, for example `/volume1/docker/itemory-agent` on Synology. **Create that folder before you start the container** (with your NAS's file manager). Keep this folder safe: it is what you back up, and moving it later makes the app treat the agent as a new server.

> [!TIP]
> The `generic.yml` template uses a Docker *named volume* instead of a folder. Docker creates and manages it for you, so there is nothing to create or set permissions on.

## Step 3: Pick the run user

For safety the container does not run as `root`. It runs as the user in the template's `user:` line, written as `"<uid>:<gid>"` (numeric user ID and group ID). That user must be able to:

- **read** your photo folders, and
- **write** the data folder from step 2.

The templates default to `"1000:1000"` (`"99:100"` on Unraid, which is the standard `nobody:users` there). This works on many systems, but not all.

The most reliable choice is **your own NAS account's IDs**, because then the agent can read exactly what you can read. If you can log in to the NAS over SSH, run:

```bash
id your-nas-username
```

The output looks like `uid=1026(me) gid=100(users) …`, which means `user: "1026:100"`. Then make sure the data folder belongs to that user:

```bash
sudo chown -R 1026:100 /volume1/docker/itemory-agent
```

No SSH? Start with the template default and continue. If something is wrong, the web console will tell you which user to use (see [Step 8](#step-8-check-that-everything-works)), and the [troubleshooting guide](troubleshooting.md#the-container-stops-right-after-starting) explains how to fix it.

## Step 4: Create the container

Pick the template for your system from [`deploy/compose/`](../deploy/compose), open it, and copy its content. Before deploying, change:

1. **The photo path** — the part *before* the first `:` in the first `volumes:` line.
2. **The data path** — the part *before* the `:` in the `/data` line (not needed for `generic.yml`).
3. **`TZ`** — your time zone, for example `Europe/Berlin` or `America/New_York`. It decides which day a photo belongs to when the photo has no time zone of its own.
4. **`user`** if you picked a different one in step 3.
5. **`mem_limit`** if your library contains videos larger than about 1 GB (see [Memory limit](configuration.md#memory-limit)).

Here is what the important lines mean:

```yaml
    user: "1000:1000"                         # step 3: who the agent runs as
    volumes:
      - /volume1:/volumes/volume1:ro          # NAS path : path inside the container : read-only
      - /volume1/docker/itemory-agent:/data   # data folder from step 2
    ports:
      - "8787:8787"                           # NAS port : container port
    environment:
      TZ: "Europe/Berlin"                     # your time zone
    mem_limit: 2g                             # memory cap, see configuration.md
```

Keep the path *inside* the container under `/volumes/…`; that is where the app looks for folders. If port 8787 is already used on your NAS, change only the number before the colon, for example `"18787:8787"`.

Then create the container. Menu names can differ slightly between system versions.

| System | Template | Where to paste it |
| --- | --- | --- |
| Synology DSM 7.2+ | `synology.yml` | Install **Container Manager** from Package Center, then *Container Manager → Project → Create*. Choose a folder for the project, select *Create docker-compose.yml*, paste, and finish the wizard. |
| QNAP QTS / QuTS hero | `qnap.yml` | *Container Station → Applications → Create*, paste, then *Create*. |
| TrueNAS SCALE 24.10+ | `truenas.yml` | *Apps → Discover Apps → Custom App*, choose installing from YAML, paste. `amd64` only. |
| Unraid | `unraid.yml` | Install the **Compose Manager** plugin from Community Applications, then *Docker → Compose → Add New Stack*, edit the stack, paste, and *Compose Up*. `amd64` only. |
| fnOS | `fnos.yml` | Open the **Docker** app → *Compose* → create a project and paste. |
| OpenMediaVault | `omv.yml` | Install the **openmediavault-compose** plugin (from omv-extras), then *Services → Compose → Files → Add*, paste, save, and *Up*. |
| Any Linux with Docker | `generic.yml` | Save it as `compose.yaml` in an empty folder and run `docker compose up -d` there. |

The first start downloads the image (about 240 MB) from `ghcr.io`. No account or login is needed.

## Step 5: Create the administrator account

On a computer on the same network, open:

```text
http://<your-NAS-IP>:8787
```

Use the NAS's real network address, not `localhost`: the address you open here is the address written into the pairing QR code.

The first visit asks you to **set up the administrator**. Choose a username (3–64 characters) and a password of **at least 12 characters**. The password is stored only as a hash and cannot be recovered. If you forget it, see [Forgot the administrator password](troubleshooting.md#forgot-the-administrator-password).

## Step 6: Pair your iPhone

1. In the web console, open **Pairing & devices** and click **Start pairing**. A QR code appears. It is valid for 5 minutes and works only once.
2. On the iPhone, open Itemory and go to **Data Sources → Itemory Private Cloud → Scan QR code**.
3. Point the camera at the QR code. The app confirms when pairing is complete.

To pair another iPhone, click **Start pairing** again for a new code.

## Step 7: Choose the folders to index

A freshly paired agent has no libraries yet. In the app:

1. Open **Itemory Private Cloud Settings → Libraries → Add Folder**.
2. Pick the folders that contain your photos and videos. They appear under `/volumes/…`, the paths you set up in step 4.
3. Save. The agent starts indexing immediately.

The first scan can take from a few minutes to several hours, depending on how many files you have and how fast the NAS is. Its progress is shown in the app's *Itemory Private Cloud Settings* and on the console's **Overview** page. After that, the agent checks for changes every night at 03:00 and only reads new or modified files.

## Step 8: Check that everything works

In the web console:

- **Overview** shows the scan progress and a list of health checks.
- **Libraries** shows every mounted folder as *Readable* or *Unreadable*. If a folder is unreadable, the **Suggested run user** under *Mounts* tells you which `user:` value would work. Put it in your template, make sure the data folder belongs to that user too, and recreate the container.
- **Diagnostics → External tools** should show both `vipsthumbnail` (photos) and `ffmpeg` (videos and Live Photos) as available.

Everything green? You're done. Next:

- [Configuration](configuration.md): tune memory, CPU and performance presets.
- [Upgrading and backup](upgrading.md): keep the agent up to date.
- [Troubleshooting](troubleshooting.md): if something doesn't work.
