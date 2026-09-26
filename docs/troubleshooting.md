# Troubleshooting

**English** | [简体中文](troubleshooting.zh-CN.md)

Start with the web console: **Overview** runs a set of health checks, and **Logs** and **Diagnostics** usually point at the cause. If the console itself won't open, check the container's log in your NAS's Docker app, or run `docker logs itemory-agent`.

**Installation**

- [The image can't be downloaded](#the-image-cant-be-downloaded)
- [The container stops right after starting](#the-container-stops-right-after-starting)
- [The web console doesn't open](#the-web-console-doesnt-open)
- [Forgot the administrator password](#forgot-the-administrator-password)

**Pairing and connecting**

- [The app can't scan or accept the QR code](#the-app-cant-scan-or-accept-the-qr-code)
- [The app can't connect after pairing](#the-app-cant-connect-after-pairing)
- [The app says the response can't be read](#the-app-says-the-response-cant-be-read)

**Folders and photos**

- [A folder shows as Unreadable](#a-folder-shows-as-unreadable)
- [My folder doesn't appear in the app](#my-folder-doesnt-appear-in-the-app)
- [Some thumbnails never appear](#some-thumbnails-never-appear)
- [Photos appear on the wrong day](#photos-appear-on-the-wrong-day)
- [Files in the data folder look unreadable on the NAS](#files-in-the-data-folder-look-unreadable-on-the-nas)

[Still stuck?](#still-stuck)

## The image can't be downloaded

- Check the spelling: `ghcr.io/sherlockgougou/itemory-agent:1`, all lowercase. The image is public, so no login is needed.
- An error like `no matching manifest` means your NAS's processor isn't supported. The image is built for `amd64` (Intel/AMD) and `arm64` (64-bit ARM). Older 32-bit ARM models can't run it.
- If `ghcr.io` is slow or unreachable from your network, configure a proxy for Docker on the NAS and try again.

## The container stops right after starting

Open the container's log. The last lines tell you why:

| Log message | Cause | Fix |
| --- | --- | --- |
| `cannot load settings: open /data/settings.json.tmp: permission denied` | The run user can't write the data folder. | Give the folder to that user (`sudo chown -R <uid>:<gid> <data folder>`), or change `user:` to the folder's owner. See [installation step 3](installation.md#step-3-pick-the-run-user). |
| `port is already allocated` or `address already in use` | Another app uses port 8787. | Change the first number in `ports`, e.g. `"18787:8787"`, and open the console on that port. |
| `no such file or directory` for a volume | The NAS path in `volumes` doesn't exist. | Fix the path, or create the data folder first. |

## The web console doesn't open

- Check that the container is running in your NAS's Docker app.
- Use `http://`, not `https://`, and the port from the first number in `ports` (8787 by default).
- Use the NAS's IP address on your network, for example `http://192.168.1.20:8787`.
- If the NAS has a firewall enabled, allow the port.

## Forgot the administrator password

The password can't be recovered, but the account can be reset without losing anything else. With SSH access to the NAS:

```bash
docker exec itemory-agent rm /data/admin.json
docker restart itemory-agent
```

Open the console again and set up a new administrator. Paired iPhones, libraries and the index are not affected.

## The app can't scan or accept the QR code

- **The code expired or was already used.** A code is valid for 5 minutes and works once. Click **Generate new** in the console.
- **The console was opened through `localhost`.** The QR code then contains an address the iPhone can't reach. Open the console through the NAS's IP address, or type that address into **Address in QR code** on the pairing page, and generate a new code.
- **The camera is blocked.** Allow camera access for Itemory in the iPhone's Settings.

## The app can't connect after pairing

- Check that the iPhone is on the same network as the NAS (not on mobile data, not on a guest Wi-Fi).
- The NAS's IP address may have changed. Reserve a fixed IP address for the NAS in your router, then pair again from the console.
- Check that the container is running and the console opens from another device.

## The app says the response can't be read

The app and the agent don't understand each other, usually because the agent is too old. [Upgrade](upgrading.md#upgrade) the agent, then try again.

## A folder shows as Unreadable

There are two possible causes:

1. **The folder isn't mounted.** The files are on the NAS, but not inside the container. Check the `volumes` lines in your template, then recreate the container.
2. **The run user has no read permission.** Open **Libraries** in the console. Under **Mounts**, the **Suggested run user** shows the owner of your folders. Put that value in `user:`, make sure the data folder belongs to the same user, and recreate the container.

Click **Re-detect** after fixing it.

## My folder doesn't appear in the app

The app only shows folders that are mounted into the container. Add a `volumes` line for the folder (for example `- /volume1/photo:/volumes/photo:ro`) and recreate the container. You can also type a path inside the container with **Enter Path Manually**.

## Some thumbnails never appear

- **Memory limit too low.** Large videos need more memory than photos. Raise `mem_limit`; see [Memory limit](configuration.md#memory-limit).
- **A tool is missing.** **Diagnostics → External tools** should show both `vipsthumbnail` (photos) and `ffmpeg` (videos and Live Photos). Both are included in the official image.
- **Unsupported file.** Some RAW files contain no embedded preview. These can't get a thumbnail.

## Photos appear on the wrong day

Set `TZ` in the template to your own time zone (for example `Europe/London`), recreate the container, and then run **Rescan everything** in the app.

## Files in the data folder look unreadable on the NAS

On some systems, including fnOS, files written by the container show `0000` permissions when you look at them from the NAS. This is how that file system presents them; the data is fine. To copy them, go through a container, as described in [Back up](upgrading.md#back-up).

## Still stuck?

[Open an issue](https://github.com/SherlockGougou/itemory-agent/issues/new) and include:

- the agent version (**Diagnostics**), your NAS model and system version;
- what you did and what happened;
- the relevant lines from **Logs** or `docker logs itemory-agent`.

Don't post passwords, pairing codes, QR codes or anything from `admin.json` / `tokens.json`. To report a security problem, follow the [security policy](../SECURITY.md) instead.
