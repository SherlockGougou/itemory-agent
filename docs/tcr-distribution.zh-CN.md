# 腾讯云国内镜像发布

[English](tcr-distribution.md) | **简体中文**

所有支持系统的 Compose 模板和自动安装器默认使用公开仓库：

```yaml
image: ccr.ccs.tencentyun.com/itemory/itemory-agent:1
```

无需用户登录。支持 `linux/amd64`、`linux/arm64`，Docker 自动选择架构。固定版本可用 `:0.5.0`；海外仍可使用 `ghcr.io/sherlockgougou/itemory-agent:1`。无法访问 Registry 时使用 [OSS 通用离线安装](oss-distribution.zh-CN.md)，OSS 文件 URL 不能直接填入 Compose `image:`。

## 发布配置

腾讯云个人版创建公开命名空间 `itemory` 和公开镜像仓库 `itemory-agent`。GitHub Actions Secrets 使用 `TCR_USERNAME`、`TCR_PASSWORD`；可选 Variable `TCR_REPOSITORY` 默认是上述地址。密码只用于发布，不能放进源码、教程或用户命令。

`release.yml` 完成 GHCR 发布后调用 `tcr-images.yml`。补发已发布版本时，手动运行 `tcr-images` 并填写完整版本。需先将工作流与发布器提交到远程仓库；仅设置 Secrets 不会自动启用本地未推送的工作流。

本地需 Python 3.9+、Docker（支持多平台镜像检查）、Skopeo 和跨架构运行能力（QEMU）。先用密码标准输入登录，再运行：

```bash
export TCR_REPOSITORY=ccr.ccs.tencentyun.com/itemory/itemory-agent
export REGISTRY_AUTH_FILE="$HOME/.docker/config.json"
python3 scripts/publish_tcr_images.py --version 0.5.0
```

发布器使用 Skopeo 将已发布 GHCR 中的两个可运行镜像转换为 Docker v2 schema 2，发布架构标签及多架构版本标签，不重新构建后端。GHCR 保留原始 provenance/SBOM。发布器逐项核对源和目标的 config/layer digest，再以空认证配置匿名拉取、运行两种架构并检查二进制版本。已有版本只有内容一致才可复用；其他错误明确失败。

仅当发布版本与当前 GHCR `:1` 完全一致且不是预发布时，才推进腾讯云 `:1`，避免补发旧版本使稳定通道降级。验收记录输出到 `.agent-temp/tcr-release/tcr-verification.json`，CI 保存为 artifact。

## 服务边界

个人版为共享服务，不提供 SLA；适合当前公开镜像分发，但不能承诺每个地区或运营商的速度。GHCR 和 OSS 保留为独立的备用来源。源切换只更改镜像，不应改照片挂载、运行用户、端口或 `/data`；已有安装优先使用自动升级以保留配对与备份。

官方依据：[腾讯云容器镜像服务](https://cloud.tencent.com/document/product/1141)、[Skopeo copy](https://github.com/containers/skopeo/blob/main/docs/skopeo-copy.1.md)、[Docker manifest](https://docs.docker.com/reference/cli/docker/manifest/)。
