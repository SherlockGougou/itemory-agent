# 参与开发

[English](CONTRIBUTING.md) | **简体中文**

欢迎提交问题报告、修复和改进。较大的改动请先开 issue，方便先就做法达成一致。

## 环境要求

- Go 1.24 或更高版本
- Node.js 22（用于管理网页）
- 可选：Docker，用于构建镜像；`libvips`（`vipsthumbnail`）和 `ffmpeg`，用于在本地生成缩略图。macOS 上处理照片时会退回到系统自带的 `sips`。

## 构建与运行

```bash
make web      # 构建管理网页；任何 Go 构建之前都必须先执行
make test     # go test ./...
make vet      # go vet ./...
make build    # 产出 dist/itemory-agent
make run      # 在本地 :8787 运行，数据写入 ./data
```

管理网页是 `internal/api/web` 下的 Next.js 静态导出。构建产物 `internal/api/web/out` 通过 `//go:embed` 嵌入二进制，不提交到仓库，所以全新 checkout 在执行 `make web` 之前无法编译。`npm run dev` 只提供页面、没有 API，想用真实数据查看管理网页的改动，请用 `make run`。

从干净的构建上下文检查 Docker 构建：

```bash
make web-check
```

## 目录结构

| 路径 | 职责 |
| --- | --- |
| `cmd/itemory-agent` | 入口：`serve`、`healthcheck`、`benchmark`、`version` |
| `internal/scan` | 按修改时间和文件大小增量扫描 |
| `internal/media` | EXIF、RAW 预览、实况照片配对、视频探测（只读取文件头尾） |
| `internal/store` | SQLite（WAL）索引 |
| `internal/thumbs` | 缩略图生成（`vipsthumbnail` → `sips` → `ffmpeg` → 纯 Go）与缓存淘汰 |
| `internal/api` | `/api/v1` REST 与 SSE、管理员会话、设备配对 |
| `internal/api/web` | 管理网页（Next.js） |
| `internal/config` | `settings.json`、性能档位与校验 |
| `deploy/compose` | 各 NAS 的 compose 模板 |
| `docs` | 用户文档 |

## 约定

- **兼容性**：Itemory App 依赖 `/api/v1`。只新增字段和接口，不修改或删除已有的。
- **格式**：执行 `gofmt`；CI 会拒绝未格式化的 Go 代码。
- **测试**：Go 测试放在被测代码旁边，命名为 `*_test.go`，并覆盖你改动的行为。
- **管理网页文案**：`internal/api/web/src/lib/i18n.json` 中每条文案都有五种语言（`zh-Hans`、`zh-Hant`、`en`、`ja`、`ko`），新增文案时五种都要补齐。
- **文档**：用户文档以英文为准，旁边提供简体中文版本（`*.zh-CN.md`）。行为变化时两份都要更新。
- **提交信息**：使用 [Conventional Commits](https://www.conventionalcommits.org/)，例如 `fix(thumbs): …` 或 `docs: …`。

## 发布

维护者通过推送版本标签发布：

```bash
git tag -a v0.4.1 -m "v0.4.1"
git push origin v0.4.1
```

随后 `.github/workflows/release.yml` 会运行测试，构建 `linux/amd64` 与 `linux/arm64` 镜像，推送 `ghcr.io/sherlockgougou/itemory-agent:<版本>` 和 `:1`，并对两个架构分别做 smoke test。

## 许可证

提交贡献即表示你同意你的贡献以 [MIT 许可证](LICENSE)发布。
