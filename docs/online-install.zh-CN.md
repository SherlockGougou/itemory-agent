# 在线安装与升级

[English](online-install.md) | **简体中文**

通过 SSH 登录 **NAS 本机**，粘贴以下命令。以后升级也执行同一条：

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash
```

脚本真源为仓库根的 [`install.sh`](../install.sh)，官网提供相同副本。

## 准备与首次安装

- 需要 Bash、Python 3.9+、curl、Docker 和 Compose V2 插件（`docker compose`）。缺少的工具通过 NAS 支持的软件包管理器或应用中心安装，脚本不会自动安装系统依赖。
- Docker 服务端需要是 Linux amd64 或 arm64，连接本机 Unix Socket。不支持远程 Docker context 和 32 位 ARM。
- 执行账号需要能操作 Docker、创建安装目录，并在 Docker Socket 旁取得安装锁。需要管理员权限时运行 `curl -fsSL https://itemory.gouqinglin.com/install.sh | sudo bash`；sudo 从终端读取密码。

脚本会询问一个或多个照片目录、**独立且为空**的安装目录、非 root `uid:gid`、服务端口（默认 8787）和时区。交互从 `/dev/tty` 读取，支持 curl 管道。照片、安装数据和备份不能互相包含，不要选择 Docker 自身的数据目录。

运行用户根据第一个照片目录的属主给出建议，再使用实际镜像和只读挂载检查根目录及样本文件的可读性；这不代表所有深层目录的 ACL 都已验证。照片默认只读，删除模式沿用服务默认值（关闭），脚本不修改照片权限或属主。只有新建的服务数据目录可能设置属主。默认限制为 1.5 CPU、2 GiB 内存；大型视频可能需要更多内存，见[配置说明](configuration.zh-CN.md#内存上限)。

脚本默认从腾讯云公开镜像仓库 `ccr.ccs.tencentyun.com/itemory/itemory-agent:1` 拉取与 Docker 服务端架构匹配的镜像，固定到该仓库的不可变 RepoDigest。无需用户登录，适用于所有支持的 NAS 与 Linux 系统。海外可用 `--source ghcr`；Registry 不可用时可显式使用 `--source oss`，从 OSS 下载镜像包、核对大小与 SHA-256、导入并检查平台和官方来源标记，再固定到本地 Image ID。来源失败会明确退出，不静默切换。见 [腾讯云镜像发布](tcr-distribution.zh-CN.md)和 [OSS 通用离线安装](oss-distribution.zh-CN.md)。

Compose JSON 写入 `<安装目录>/.itemory-online/<时间-id>/compose.json`，启动时禁止联网拉取，再检查健康状态。打开输出的管理网页地址，创建管理员账号，在 Itemory 中扫码配对并添加媒体库。容器内照片路径为 `/volumes/photos1`、`/volumes/photos2` 等。脚本不创建账号或配对信息，不修改防火墙或路由器；请仅向可信局域网提供访问。

仅 `--source oss` 使用镜像包缓存，默认缓存在执行账号的 `~/.cache/itemory-agent/downloads/`，可用 `--download-dir <绝对路径>` 更改；请保留下载与 Docker 导入所需磁盘空间，不选择照片或服务数据目录。缓存命中后仍核对 SHA-256，不自动删除镜像或缓存。下载有连接、低速和总时长限制，失败会明确退出，不会静默改用 GHCR。

需要原有 GHCR 路径时显式指定：

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --source ghcr
```

通用离线包也可以手动导入，见 [OSS 离线安装](oss-distribution.zh-CN.md)。`--source oss` 示例：

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --source oss
```

`--download-base https://<下载域名>/<发布目录>` 可指定另一处同格式的 HTTPS 发布目录；目录不能含查询参数或凭据。校验和用于检查下载完整性，信任来源仍是发布服务器的 HTTPS 和发布账号。

希望先审查脚本时，可以下载到自己的目录，阅读后再执行 `bash install.sh`。

## 已有安装与升级

脚本识别官方镜像容器，包括既有 Compose 模板创建的容器，从 Docker labels 取得原配置文件、项目名和工作目录。原文件必须仍存在，解析后的配置 hash 必须与容器一致。配置缺失、配置发生变化、数据布局不支持或检测到多个安装时，**停服前就会退出**。Compose 自身升级也可能改变 hash，需要用原 Compose 版本核对并同步配置与运行状态后再重试。通过 `docker run` 创建的容器需要手动迁移，脚本不会猜测部署配置。

升级步骤：

1. 先拉取目标镜像（OSS 路径下载、校验并导入），成功后才考虑停止服务。
2. 用户确认后，只停止目标容器，完整备份 `/data`，包括 SQLite WAL、设置、管理员账号、配对身份和缩略图。
3. 在安装记录目录保存解析后的 Compose 配置、容器信息和备份。目录权限 700、文件 600；其中包含敏感信息及当前配置，不要删除或公开。脚本不会清理旧备份或镜像。
4. 保留原项目、数据卷、照片路径、运行用户、环境变量、端口和资源限制，只替换目标服务的不可变镜像引用。**新安装默认只读**；旧安装已经显式配置的可写照片挂载保持原状。
5. 检查健康状态、`/data` 来源、管理员凭据、`serverId`、已配对设备令牌 hash 和已有设置。重启后网页管理员会话失效，需要使用原账号重新登录；手机无需重新配对。

已经是目标镜像时只做健康检查，不重建容器；原容器停止时，确认后可以启动。多个安装时指定容器：

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --container itemory-agent
```

指定版本：

```bash
curl -fsSL https://itemory.gouqinglin.com/install.sh | bash -s -- --version 0.5.0
```

`--yes` 跳过确认，全新安装时必须提供照片路径。用 `--help` 查看参数。升级不接受新的照片目录、用户等安装参数；有意修改部署设置时，单独编辑当前 Compose 配置。

## 失败与恢复

拉取镜像或预检失败时，原服务不变。停服后备份失败，脚本尝试启动原容器；替换或验收失败，脚本尝试停止目标服务并输出备份目录，明确报告失败。**不会宣称已经自动回滚**，因为新服务可能已迁移数据库。

恢复时保留失败后的数据，使用停服时的 `data.tar`：

1. 查看记录目录中的 `before-container.json` 和 `before-compose.json`，后者固定到升级前的本地镜像 ID。不要清理该镜像。
2. 将 `data.tar` 还原到**新建的空目录**：`tar -xpf data.tar -C <恢复目录>`，使用适当权限。不要覆盖失败数据，也不要解压到照片目录。
3. 复制 `before-compose.json` 为恢复配置，只把目标服务的 `/data` 挂载改成恢复目录的 bind mount，保留原运行用户及照片挂载路径。
4. 执行 `docker compose --project-directory <原目录> -p <原项目> -f <恢复配置> up -d --no-deps --no-build --pull never <服务名>`。检查健康状态、日志、设置和手机读取后，再考虑清理恢复材料。

服务使用安装记录目录里的配置时，不要删除这个目录。安装器不执行可能删除命名卷的 `docker compose down -v`。
