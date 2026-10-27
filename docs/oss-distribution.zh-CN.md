# OSS 镜像包发布与全系统离线安装

[English](oss-distribution.md) | **简体中文**

在线安装默认使用[腾讯云公开镜像](tcr-distribution.zh-CN.md)，OSS 是所有系统通用的离线备用方案。OSS 用于分发 `docker save` 镜像包，不是 Docker Registry。用户只通过 CDN 域名下载，OSS Bucket 保持私有。首次启用前先完成上传凭据、CDN 私有 Bucket 回源和公网下载验收；未完成这些步骤前，不应对用户宣称下载入口可用。

## 发布者配置

在 GitHub 仓库 Settings → Secrets and variables → Actions 配置以下 Variables：

| 名称 | 本次使用的值 |
| --- | --- |
| `OSS_BUCKET` | `itemory-downloads` |
| `OSS_REGION` | `cn-chengdu` |
| `OSS_PREFIX` | `releases/itemory-agent` |
| `OSS_DOWNLOAD_BASE` | `https://cdn.gouqinglin.com/releases/itemory-agent` |

在同一页面的 Secrets 设置 `OSS_ACCESS_KEY_ID`、`OSS_ACCESS_KEY_SECRET`；使用临时凭据时还需 `OSS_SESSION_TOKEN`。不要将凭据写进代码、Compose 或面向用户的安装命令。上传账号只需要此发布前缀的 `oss:PutObject`，不需要读取或修改其他目录，也不需要修改 Bucket ACL。CI 使用官方 ossutil 2.4.0，并校验官方发布的 SHA-256；不引入第三方上传 Action。

使用独立的下载 Bucket，并保持 ACL 私有和“阻止公共访问”开启，不添加匿名 `oss:GetObject` 授权。CDN 加速域名以 `itemory-downloads.oss-cn-chengdu.aliyuncs.com` 为源站，开启“阿里云 OSS 私有 Bucket 回源”，只允许用户访问发布目录。该功能使用系统角色 `AliyunCDNAccessingPrivateOSSRole`，只给 CDN 授予 OSS 只读权限，不能授予写入或删除权限。配置 CDN 流量和 HTTPS 请求数封顶，避免异常访问产生高额费用。

对象应使用标准存储；归档类型需要先恢复，不适合即时安装。安装器无须访问密钥，OSS 默认域名必须继续返回 403。CDN 私钥和证书不进入仓库；临时签名链接、匿名 Bucket Policy 公共授权都不作为长期发布地址。

## 发布流程

仅在配置好上传凭据后，将 Actions Variable `OSS_PUBLISH_ENABLED` 设为 `true`，正式发布才会自动调用 OSS 工作流。未启用时腾讯云同步照常执行，OSS 手动上传不受影响。

正常服务发布通过 `.github/workflows/release.yml` 构建、测试 GHCR 多架构镜像后，再调用 `oss-images.yml`。为已有 GHCR 版本补发 OSS 镜像包时，在 Actions 中手动运行 `oss-images`，填写完整版本（例如 `0.5.0`），不需要重新发布或重新构建后端。

发布器需要支持 `docker image inspect --platform` 的 Docker API 1.49+、Python 3.9+，跨架构版本检查需要 QEMU。只生成本地文件、不上传：

```bash
python3 -m pip install -r scripts/requirements-publish.txt
python3 scripts/publish_oss_images.py --version 0.5.0 --output .agent-temp/oss-0.5.0 --package-only
```

每个版本包含两种架构的 `.tar.gz`、校验文件、通用 `-offline.zip` 与 `manifest.json`。通用 ZIP 内有镜像包、SHA256SUMS、`compose/` 下全部九种固定版本模板和中英文说明；模板禁止联网拉取。镜像来自已发布 GHCR，不重新构建，并检查平台、来源标记与二进制版本。

产物首先上传到 `versions/<version>/`，使用禁止覆盖选项；重试只接受大小和 SHA-256 元数据一致的现有对象，没有元数据时完整下载核对 SHA-256。通过 CDN 完整下载并核对两种架构的镜像包后，才更新 `channels/1.json`、`channels/1/itemory-agent-<arch>-offline.zip` 及对应 `.sha256`。0.x 正式版本沿用现有 `:1` 稳定通道；预发布版本不更新稳定通道。若下载验证失败，工作流失败且不更新安装器通道；已上传文件保留以便重试。

## 所有系统离线安装

1. 确认 Docker 服务端架构：Intel/AMD、`x86_64` 对应 amd64；64 位 ARM、`aarch64` 对应 arm64。
2. 从官网安装页下载对应架构的通用 ZIP 并解压。用 NAS 的本地镜像导入功能，或执行 `docker image load -i itemory-agent-<版本>-<架构>.tar.gz`。若 GUI 只接受 `.tar`，先解压 gzip。不要使用 `docker import`。
3. 从包内 `compose/` 选择系统模板：`ugreen.yml`、`synology.yml`、`fnos.yml`、`qnap.yml`、`unraid.yml`、`truenas.yml`、`omv.yml`、`zspace.yml` 或 `generic.yml`。核对实际照片与数据目录、`uid:gid` 和端口；保留固定镜像版本与 `pull_policy: never`。按对应系统教程创建项目；CLI 可用 `docker compose -f compose/<系统>.yml up -d --pull never`。
4. 打开 `http://<NAS局域网IP>:8787`，创建管理员并用 Itemory 扫码配对。新安装照片默认只读，不用开启特权模式。

已有安装优先使用[自动升级](online-install.zh-CN.md)，先备份 `/data` 并保留原挂载和配对；不要直接用新安装模板覆盖原配置。

## 验证

```bash
bash -n install.sh
python3 scripts/test_online_installer.py
python3 scripts/test_oss_installer_docker.py --packages .agent-temp/oss-0.5.0
```

集成测试通过受信任的本地 HTTPS 下载真实镜像包，执行新装、重复安装、旧 GHCR Compose 升级、配对保留和配置漂移拒绝。它不证明阿里云 OSS + CDN 实际链路、国内下载速度或真实 UGOS 界面已经通过验收；后者分别需要在线 CDN 检查与绿联设备验收。

依据：[OSS 官方 ossutil](https://help.aliyun.com/zh/oss/developer-reference/ossutil-overview/)、[Docker save](https://docs.docker.com/reference/cli/docker/image/save/)、[Docker load](https://docs.docker.com/reference/cli/docker/image/load/)、[绿联镜像管理](https://support.ugnas.com/detail/article/en-US/290)。
