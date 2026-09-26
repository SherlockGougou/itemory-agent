# 部署到 NAS

`compose/` 下是各 NAS 的推荐模板，镜像为 `ghcr.io/sherlockgougou/itemory-agent:1`（`linux/amd64` 与 `linux/arm64`）。

| 模板 | 适用环境 |
| --- | --- |
| `synology.yml` | 群晖 DSM 7 · Container Manager |
| `qnap.yml` | 威联通 QTS · Container Station |
| `truenas.yml` | TrueNAS SCALE（仅 amd64） |
| `unraid.yml` | Unraid（仅 amd64） |
| `fnos.yml` | 飞牛 fnOS |
| `omv.yml` | OpenMediaVault · compose 插件 |
| `generic.yml` | 其他支持 Docker Compose 的环境 |

## 首次部署

1. 把模板里的媒体卷改成 NAS 上的实际路径。建议只读挂载卷根目录，之后在 Itemory App 里选择具体文件夹作为媒体库。
2. 确认数据目录（容器内 `/data`）对 `user` 指定的用户可写。群晖、威联通等使用宿主目录的模板，需要先创建目录并把属主设为该用户；`generic.yml` 使用命名卷，不需要这一步。Unraid 模板默认使用 `99:100`（nobody:users），其余模板为 `1000:1000`，按 NAS 上实际的用户 ID 调整。
3. 按下文[资源上限](#资源上限)调整 `mem_limit`。
4. 部署后浏览器打开 `http://<NAS 地址>:8787`，首次访问时创建管理员账号，然后在「配对与设备」页生成二维码给 App 扫描。

## 升级

`:1` 标签始终指向最新发布的版本（API v1）。

```bash
docker compose pull
docker compose up -d
```

升级前建议备份数据目录（索引、设置、管理员账号与已配对设备都在其中）。数据目录保留不变时，已配对的 App 不需要重新配对。

## 回滚

每个版本都有独立标签，例如 `ghcr.io/sherlockgougou/itemory-agent:0.4.0`。把 compose 里的 `image` 改成上一个版本的标签，再执行 `docker compose up -d`。如果升级前做过数据目录备份，回滚时一并恢复。

## 升级后核对

- `GET /api/v1/health` 返回的 `version` 是新版本，`serverId` 与升级前一致。`serverId` 变了说明数据目录挂错了，App 会认为这是一台新服务。
- 未登录时 `POST /api/v1/scan` 与 `GET /api/v1/admin/overview` 返回 401，`GET /api/v1/dashboard` 返回 200。三条一起成立，说明鉴权分层正常。
- 控制台「媒体库」中各路径显示为可读，「诊断」页的外部工具全部可用（vipsthumbnail 负责图片，ffmpeg 负责视频与动态照片）。

## 安全参数

模板默认启用以下参数，服务在这些限制下可以正常运行：

| 参数 | 作用 |
| --- | --- |
| `read_only: true` | 容器根文件系统只读，所有写入都在 `/data` |
| `tmpfs: /tmp` | 给图像解码的临时文件使用；库里 HEIC / RAW 较多时可调到 `256m` |
| `cap_drop: [ALL]` | 去掉全部 Linux capability |
| `no-new-privileges` | 禁止进程提权 |
| 媒体卷 `:ro` | 服务不会修改任何照片或视频 |

## 资源上限

内存是模板参数里唯一必须按实际情况调整的一项。上限太低时，缩略图或视频封面生成进程会被 OOM 终止，而且失败是静默的：界面上只表现为部分缩略图一直不出现。

### 决定内存需求的是最大的媒体文件

实测（24 核 x86_64 NAS）：单独一次 2 GB 视频抽帧，512m 失败，768m 成功，1g、2g 都成功。并发数不是主要变量：缩略图并发已封顶为 8（`internal/thumbs/thumbs.go` 的 `maxThumbWorkers`），降低并发并不能让大视频在 512m 下成功。

| 场景 | 结果 |
| --- | --- |
| 48 路并发 + 512m | 视频抽帧全部被终止，部分图片也被终止 |
| 8 路并发 + 512m | 视频仍失败 |
| 8 路并发 + 1g | 视频全部成功 |
| 48 路并发 + 1g | 视频全部成功 |

判断内存是否够用时，要看任务有没有被终止，不要看 cgroup 的 `memory.peak`。cgroup v2 会把文件页缓存计入容器内存，这个读数会随并发反向变化，不能反映真实需求。

按库里最大的单个文件选择 `mem_limit`：

| 最大的单个文件 | 建议 `mem_limit` |
| --- | --- |
| ≤ 100 MB（纯照片） | 512m |
| ≤ 500 MB | 1g |
| 1–2 GB | 2g（模板默认值） |
| > 2 GB | 4g 或更高 |

### CPU

`cpus` 只影响速度，不影响结果。模板取 1.5 核，避免整库首次扫描时影响 NAS 上的其他服务；同一批缩略图任务在 1.5 核下比不限制时慢一个数量级。首次建库较慢时可以临时调高，建完后再调回。

## 常见问题

**宿主上读不了数据目录里的文件。** 部分 NAS 系统（例如 fnOS）会把容器写入的文件在宿主上显示为 `0000` 权限，宿主上的 `cp`、`sha256sum` 甚至 `rm` 都会失败。这不是数据损坏。需要读取或备份时通过容器操作：

```bash
docker run --rm -v <数据目录>:/mnt:ro alpine ls -l /mnt
```

**App 提示版本不一致或内容无法识别。** 执行上文的升级步骤，确认 `/api/v1/health` 的 `version` 是最新版本。
