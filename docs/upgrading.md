# Upgrading and backup

**English** | [简体中文](upgrading.zh-CN.md)

- [Check your version](#check-your-version)
- [Upgrade](#upgrade)
- [Back up](#back-up)
- [Restore or roll back](#restore-or-roll-back)
- [Uninstall](#uninstall)

## Check your version

Open the web console and go to **Diagnostics**; the **Service instance** card shows the **Version** and the **Instance ID**. You can also open `http://<your-NAS-IP>:8787/api/v1/health` in a browser and look for `"version"`.

All published versions are listed on the [tags page](https://github.com/SherlockGougou/itemory-agent/tags) and the [package page](https://github.com/SherlockGougou/itemory-agent/pkgs/container/itemory-agent).

## Upgrade

The templates use the `:1` tag, which always points to the latest release. Upgrading means downloading the newest image and recreating the container. Your data folder is kept, so **you don't need to pair again or re-select folders**.

- **In a NAS app** (Container Manager, Container Station, Compose Manager and similar): open the project and use its *pull* / *update* / *rebuild* action, which downloads the new image and recreates the container. The exact name differs between systems.
- **On the command line**, in the folder that contains the compose file:

  ```bash
  docker compose pull
  docker compose up -d
  ```

After upgrading, check that:

- the version in **Diagnostics** is the new one;
- the **Instance ID** is the same as before. If it changed, the container is using a different data folder than before, and the app will treat it as a new server. Fix the `/data` line in your template.
- **Libraries** still shows your folders as readable.

Making a [backup](#back-up) before upgrading is a good habit.

## Back up

Everything the agent knows lives in its data folder (`/data` inside the container):

| File | Contents | Needed in a backup? |
| --- | --- | --- |
| `admin.json` | Administrator account (password stored as a hash) | Yes |
| `tokens.json` | Paired devices and the instance ID | Yes |
| `settings.json` | Libraries and all settings | Yes |
| `index.sqlite` (plus `-wal` / `-shm`) | The photo index | Yes, or let it rebuild with a full scan |
| `thumbs/` | Thumbnail cache | No, thumbnails are regenerated on demand |

To make a consistent copy:

1. Stop the container.
2. Copy the whole data folder somewhere safe.
3. Start the container again.

Copying while the container runs can capture the index halfway through a write.

> [!NOTE]
> On some systems, including fnOS, files written by the container look unreadable (`0000` permissions) from the NAS itself, so a normal copy fails. Copy them through a temporary container instead, for example:
>
> ```bash
> docker run --rm -v /vol1/1000/itemory-agent/data:/from:ro -v /vol1/1000/backup:/to alpine cp -a /from/. /to/
> ```

## Restore or roll back

**To restore a backup**: stop the container, replace the data folder's content with the backup, and start it again. The app reconnects without pairing again, because the instance ID and paired devices come from the backup.

**To go back to an older version**: change the `image` line from `:1` to a specific version, for example:

```yaml
    image: ghcr.io/sherlockgougou/itemory-agent:0.4.0
```

then recreate the container. A newer version may have upgraded the index, so restore the backup you made **before** the upgrade together with the older version. To follow the latest releases again later, change the tag back to `:1`.

## Uninstall

1. In the app, remove the data source under **Data Source Settings → Delete This Source**.
2. On the NAS, stop and delete the container or project.
3. Delete the data folder (or, for `generic.yml`, the Docker named volume) if you don't want to keep the index.

Your photos are never touched: they were mounted read-only the whole time.
