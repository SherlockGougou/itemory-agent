# 0.6.0 后端功能与改动整理

[English](development-status.md) | **简体中文**

整理日期：2026-10-18。本文记录 0.6.0 的功能范围与验证边界；正式发布状态与镜像地址见 GitHub Release。

## 功能范围

| 功能 | 代码与行为 | 状态 |
| --- | --- | --- |
| 媒体删除 | 新增已认证的 `DELETE /api/v1/items/{id}`；`deleteMode` 支持 `off`、`trash`、`permanent`，默认 `off`。只有显式开启且照片目录可写时才执行删除；开启时 health/pair 才下发 `delete` 能力位。 | 0.6.0 |
| 回收站 | `trash` 移到所属媒体库的 `.itemory-trash/`，保持子目录结构，避免覆盖同名文件；扫描跳过回收站。还原需手动移回原目录后扫描。 | 0.6.0 |
| 路径与权限校验 | 校验配置库、解析后的真实路径及源目录可写性；回收站根与嵌套目录拒绝符号链接、断链和非目录组件。库根可写但照片子目录只读时返回 `read_only_mount`。 | 0.6.0 |
| 实况照片与索引收敛 | 删除静帧时连带处理关联视频；片段 ID 回落到所属静帧。文件操作后清理缩略图、标记索引 removed、广播 `library/changed`。后续清理失败或片段失败通过 warnings 返回，不把已完成的文件删除伪装成未发生；整对文件不承诺事务原子性。 | 0.6.0 |
| 设置兼容 | 旧客户端 PUT 未携带 `deleteMode` 时保留当前值；PATCH 支持显式更新。缺失或未知设置归为 `off`，旧 settings.json 不会意外开启删除。 | 0.6.0 |
| 扫描完整性 | health 和扫描状态新增 `scan.complete`：仅成功完成全量/增量扫描并收敛索引后为 true；新进程、扫描中、失败、取消和不完整扫描均为 false。供客户端判断整天原片清单是否可用；没有新增批量删除 API。 | 0.6.0 |
| 国内镜像 | 所有 Compose 模板及安装器默认使用腾讯云公开仓库，GHCR 为海外来源；0.6.0 支持 amd64/arm64。发布器核对 config/layer digest，并匿名拉取、核对 RootFS 与运行版本后更新稳定标签。 | 已上线，自动同步工作流已推送 |
| 自动安装与升级 | 支持本机 Compose 安装；新装照片只读，升级备份 `/data` 并保留挂载、账号、配对、设置及运行参数；配置漂移停服前拒绝。显式选择 `tcr`、`ghcr` 或 `oss`，失败不静默切换。 | 安装器已上线 |
| OSS 通用离线包 | amd64/arm64 镜像包、SHA-256 和九种系统模板；离线模板固定版本并禁止联网拉取。保留旧绿联 ZIP。 | 0.6.0 离线分发；自动上传需凭据及 `OSS_PUBLISH_ENABLED=true` |

删除接口对应 `internal/api/delete.go`、`server.go`、`handlers.go`；设置在 `internal/config/`；索引与缓存清理在 `internal/store/`、`internal/thumbs/`；完整性状态在 `internal/scan/`。分发与安装代码在 `.github/workflows/`、`scripts/`、`install.sh`、`deploy/compose/`。

## 合并范围

保留本地已有删除功能、文档与只读错误修复的三个提交历史，将远程 main 已发布的国内镜像优化合入，再纳入回收站路径防护、扫描完整性及配套测试和中英文文档。发布版本为 v0.6.0，国内镜像与 GHCR 使用同一来源的双架构镜像。

服务发布不代表客户端功能已经上架。旧客户端保持读取兼容；删除入口取决于客户端版本及服务能力位。网站继续按 App Store 已上线客户端描述，不提前宣传尚未上架的客户端删除交互。

## 验证与边界

- `make web && make test && make vet`：通过，包含管理网页静态构建、全量 Go 测试与 vet。
- 删除相关测试覆盖默认关闭、鉴权、回收站、库外路径、符号链接、只读根/子目录、实况关联、索引隐藏、缩略图失效与旧客户端设置兼容；扫描测试覆盖成功、取消、失败和不完整扫描的 `complete`。
- `bash -n install.sh`、`python3 scripts/test_online_installer.py`（17 项）、`python3 -m py_compile scripts/*.py`、`gofmt -l cmd internal` 与 Git diff 检查通过。
- 合并后核对所有已有文件与本轮已测试快照逐字节一致，另新增本整理文档及英文版。
- 未执行真实 NAS 删除、客户端整天删除及新开发镜像部署验收。此前已上线 0.5.0 的镜像拉取/安装测试不能代替新删除代码的设备验证。
- `npm ci` 报告现有锁定依赖的 13 项安全告警（2 moderate、10 high、1 critical）；本次未修改依赖及锁文件，依赖升级作为后续事项。

使用说明：[删除照片](configuration.zh-CN.md#删除照片)、[在线安装](online-install.zh-CN.md)、[腾讯云发布](tcr-distribution.zh-CN.md)、[OSS 分发](oss-distribution.zh-CN.md)。
