# Troubleshooting

**English** | [简体中文](troubleshooting.zh-CN.md)

Start with the web console: **Overview** runs a set of health checks, and **Logs** and **Diagnostics** usually point at the cause. If the console itself won't open, check the container's log in your NAS's Docker app, or run `docker logs itemory-agent`.

**Installation**

- [The image can't be downloaded](#the-image-cant-be-downloaded)
- [The container can't be created](#the-container-cant-be-created)
- [The container stops right after starting](#the-container-stops-right-after-starting)
- [The web console doesn't open](#the-web-console-doesnt-open)
- [Forgot the administrator password](#forgot-the-administrator-password)

**Pairing and connecting**

- [The app can't scan or accept the QR code](#the-app-cant-scan-or-accept-the-qr-code)
- [The app can't connect after pairing](#the-app-cant-connect-after-pairing)
- [The app shows an error number](#the-app-shows-an-error-number)
- [The app says the response can't be read](#the-app-says-the-response-cant-be-read)

**Folders and photos**

- [A folder shows as Unreadable](#a-folder-shows-as-unreadable)
- [My folder doesn't appear in the app](#my-folder-doesnt-appear-in-the-app)
- [Thumbnails load slowly the first time](#thumbnails-load-slowly-the-first-time)
- [Some thumbnails never appear](#some-thumbnails-never-appear)
- [Photos appear on the wrong day](#photos-appear-on-the-wrong-day)
- [Videos appear on the wrong day](#videos-appear-on-the-wrong-day)
- [Live Photos play as still photos](#live-photos-play-as-still-photos)
- [Files in the data folder look unreadable on the NAS](#files-in-the-data-folder-look-unreadable-on-the-nas)

[Still stuck?](#still-stuck)

## The image can't be downloaded

- Check the spelling: `ghcr.io/sherlockgougou/itemory-agent:1`, all lowercase. The image is public, so no login is needed.
- An error like `no matching manifest` means your NAS's processor isn't supported. The image is built for `amd64` (Intel/AMD) and `arm64` (64-bit ARM). Older 32-bit ARM models can't run it.
- If `ghcr.io` is slow or unreachable from your network, configure a proxy for Docker on the NAS and try again.

## The container can't be created

If the container never appears and Docker reports `Range of CPUs is from 0.01 to 1.00, as there are only 1 CPUs available`, the machine has a single CPU core and the template asks for 1.5. Change `cpus` to `"1"`, or delete the line, and create the container again.

For errors about a port or a folder path, see the table in the next section.

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

## The app shows an error number

When the agent answers with an error, the app shows *Itemory Private Cloud didn't respond properly (error …)*. The number tells you what happened:

| Number | Meaning | Fix |
| --- | --- | --- |
| 401 | The agent no longer knows this iPhone. Either the device was revoked in the console under **Pairing & devices**, or the container now uses a different data folder (the **Instance ID** in **Diagnostics** has changed). | Pair again from the console. If the data folder changed by mistake, fix the `/data` line in your template instead; the old pairing then works again. |
| 410 | The pairing QR code has expired or was already used. | Click **Generate new** in the console and scan the new code. |
| 429 | Too many pairing attempts with a wrong code. | Wait one minute, then generate a new code. |
| 500 or higher | The agent failed while handling the request. | Open **Logs** in the console and look at the lines from that moment. |

## The app says the response can't be read

The app and the agent don't understand each other, usually because the agent is too old. [Upgrade](upgrading.md#upgrade) the agent, then try again.

## A folder shows as Unreadable

There are two possible causes:

1. **The folder isn't mounted.** The files are on the NAS, but not inside the container. Check the `volumes` lines in your template, then recreate the container.
2. **The run user has no read permission.** Open **Libraries** in the console. Under **Mounts**, the **Suggested run user** shows the owner of the folder that can't be read. Put that value in `user:`, make sure the data folder belongs to the same user (`sudo chown -R <uid>:<gid> <data folder>`), and recreate the container.

   No suggestion is shown when the folder belongs to `root`, or when it is protected by an access-control list rather than by its owner (common for Synology and QNAP shared folders). In that case, give your NAS account read permission on the shared folder in the NAS's own settings, and use that account's IDs in `user:` as described in [installation step 3](installation.md#step-3-pick-the-run-user).

Click **Re-detect** after fixing it.

This also covers a mount that is readable while a folder inside it isn't, which is typical when you mount a whole volume: the folder is simply missing from the list in the app. The suggestion in the console takes those folders into account.

## My folder doesn't appear in the app

The app only shows folders that are mounted into the container. Add a `volumes` line for the folder (for example `- /volume1/photo:/volumes/photo:ro`) and recreate the container. You can also type a path inside the container with **Enter Path Manually**.

## Thumbnails load slowly the first time

A photo whose thumbnail hasn't been made yet gets one the moment the app asks for it. That takes the NAS a moment per photo, so a day you open for the first time can fill in gradually. The next time it is instant.

The agent also makes thumbnails ahead of time after each scan, up to the **Nightly thumbnail limit**. Right after the first scan of a large library, most thumbnails are therefore still missing. To catch up faster:

- raise **Nightly thumbnail limit** in the app and run **Scan for new items**; generation starts again when the scan ends;
- raise `cpus` in the template for the time being (see [CPU limit](configuration.md#cpu-limit)).

**Logs** in the console shows a line `thumbnail pre-generation finished` after each round, with the number of thumbnails made and the reason it stopped: `done` (nothing is missing), `budget` (the nightly limit was reached) or `cache` (the cache is nearly full; raise **Private cloud cache limit**). See [Background thumbnails](configuration.md#background-thumbnails).

## Some thumbnails never appear

- **Memory limit too low.** Large videos need more memory than photos. Raise `mem_limit`; see [Memory limit](configuration.md#memory-limit).
- **A tool is missing.** **Diagnostics → External tools** should show both `vipsthumbnail` (photos) and `ffmpeg` (videos and Live Photos). Both are included in the official image.
- **Unsupported file.** Some RAW files contain no embedded preview. These can't get a thumbnail.

## Photos appear on the wrong day

Set `TZ` in the template to your own time zone (for example `Europe/London`), recreate the container, and then run **Rescan everything** in the app.

## Videos appear on the wrong day

Videos don't carry the same date information as photos. The agent takes a video's date from the first of these that exists:

1. a date in the file name, such as `VID_20230506_150809.mp4`;
2. the creation time stored inside the video file by the camera;
3. the file's modification time on the NAS.

If a video shows up on the day you copied it to the NAS, neither of the first two was available and the modification time was changed by the copy. Agent versions up to 0.4.0 didn't read the creation time inside the file at all; [upgrade](upgrading.md#upgrade), and the next scan corrects the affected videos by itself.

If videos are off by a few hours and land on the neighbouring day, check `TZ` as described in [Photos appear on the wrong day](#photos-appear-on-the-wrong-day).

## Live Photos play as still photos

The agent recognises a Live Photo when the photo and its video are **in the same folder and have the same name**, for example `IMG_1234.HEIC` and `IMG_1234.MOV`. Check that:

- both files were copied to the NAS. Some export and backup tools keep only the photo;
- they weren't renamed differently or sorted into separate folders;
- **Live Photo detection** is turned on in the app's *Itemory Private Cloud Settings*.

After fixing the files, run **Scan for new items** in the app.

## Files in the data folder look unreadable on the NAS

On some systems, including fnOS, files written by the container show `0000` permissions when you look at them from the NAS. This is how that file system presents them; the data is fine. To copy them, go through a container, as described in [Back up](upgrading.md#back-up).

## Still stuck?

[Open an issue](https://github.com/SherlockGougou/itemory-agent/issues/new) and include:

- the agent version (**Diagnostics**), your NAS model and system version;
- what you did and what happened;
- the relevant lines from **Logs** or `docker logs itemory-agent`.

Don't post passwords, pairing codes, QR codes or anything from `admin.json` / `tokens.json`. To report a security problem, follow the [security policy](../SECURITY.md) instead.
