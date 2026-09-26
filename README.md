# Itemory Agent

[![ci](https://github.com/SherlockGougou/itemory-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/SherlockGougou/itemory-agent/actions/workflows/ci.yml)

Itemory Agent 是 Itemory 相册 App 的 NAS 增强服务。它在 NAS 上以 Docker 容器运行，增量扫描照片与视频、预先生成缩略图，并通过局域网 API 提供给 Itemory App，读取速度比直接走 SMB / WebDAV 快。

> Itemory Agent is the NAS companion service for the Itemory photo app. It runs as a Docker container, incrementally indexes photos and videos, pre-generates thumbnails, and serves them to the app over a LAN API.

## 功能

- 按修改时间与文件大小增量扫描，只读取文件头尾提取 EXIF、拍摄时间、GPS 与视频时长。
- 支持 HEIC、RAW 内嵌预览、动态照片配对。
- 缩略图依次尝试 libvips、sips 和纯 Go 实现，视频封面使用 ffmpeg。
- 内置网页管理控制台：媒体库可读性、扫描进度、缓存、日志、诊断和设备配对。媒体库由 Itemory App 配置。
- 媒体目录只读挂载，索引与缓存只写入 `/data`。

## 安装

镜像：`ghcr.io/sherlockgougou/itemory-agent:1`，支持 `linux/amd64` 与 `linux/arm64`。

1. 从 [`deploy/compose/`](deploy/compose) 选择对应 NAS 的模板：群晖（`synology.yml`）、威联通（`qnap.yml`）、TrueNAS SCALE（`truenas.yml`）、Unraid（`unraid.yml`）、飞牛 fnOS（`fnos.yml`）、OpenMediaVault（`omv.yml`），其他环境使用 `generic.yml`。
2. 把模板里的媒体卷路径改成 NAS 上的实际路径，按[资源上限](deploy/README.md#资源上限)调整 `mem_limit`，然后在 NAS 的 Docker / Compose 界面中部署。
3. 浏览器打开 `http://<NAS 地址>:8787`，首次访问时创建管理员账号。
4. 在控制台「配对与设备」页点按「开始配对」生成二维码（5 分钟内有效，只能使用一次），然后在 Itemory App 的「数据源」中添加增强服务并扫描该二维码。

升级、回滚与资源调整见 [deploy/README.md](deploy/README.md)。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ITEMORY_CACHE_DIR` | 镜像内为 `/data` | 索引、缓存与凭据的存放目录 |
| `ITEMORY_HTTP_ADDR` | `:8787` | 监听地址 |
| `ITEMORY_PRESET` | `balanced` | 资源档位：`light`、`balanced`、`performance`，只在首次启动时生效，之后在控制台修改 |
| `TZ` | — | 容器时区，影响按自然日分组 |

## 从源码构建

需要 Go 1.24 与 Node.js 22。

```bash
make web      # 构建管理控制台（Next.js 静态导出），Go 编译前必须先执行
make test     # go test ./...
make vet
make build    # 产出 dist/itemory-agent
make run      # 本地运行，监听 :8787，数据写入 ./data
```

控制台由 `internal/api/web.go` 通过 `//go:embed all:web/out` 嵌入二进制，`web/out` 不进版本库。

## 目录结构

| 目录 | 职责 |
| --- | --- |
| `cmd/itemory-agent` | 入口：`serve`、`healthcheck`、`benchmark`、`version` |
| `internal/scan` | 增量扫描 |
| `internal/media` | EXIF、RAW 预览、动态照片配对、视频探测 |
| `internal/store` | SQLite（WAL）索引 |
| `internal/thumbs` | 缩略图生成与缓存淘汰 |
| `internal/api` | `/api/v1/*` REST 与 SSE，管理员认证与设备配对 |
| `internal/api/web` | Next.js 管理控制台 |
| `deploy/compose` | 各 NAS 的 compose 模板 |

## 发布

推送 `v*` tag（例如 `v0.4.1`）后，`.github/workflows/release.yml` 会先运行测试，再构建 amd64 / arm64 多架构镜像，推送 `ghcr.io/sherlockgougou/itemory-agent:<版本>` 与 `:1`，并对两个架构各跑一次 smoke test。

## 许可证

[MIT](LICENSE)
