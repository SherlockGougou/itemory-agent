# 让 AI Agent 帮你部署

[English](deploy-with-ai-agent.md) | **简体中文**

如果你在用能执行终端命令的 AI Agent（例如能通过 SSH 连到你 NAS 的编程类 Agent），可以让它替你完成安装。复制下面的提示词，把开头你知道的信息填上，然后发给你的 Agent。

只能聊天、不能执行命令的 AI 助手也可以用这段提示词：它会改为一步一步指导你操作。

## Agent 会做什么、不会做什么

提示词里写明了边界，主动权始终在你手里：

- Agent **会**检查 NAS 环境、创建服务的数据文件夹、写好 compose 文件、启动容器并验证是否正常。
- Agent **不会**动你的照片文件夹：照片只以只读方式挂载。
- Agent 在使用 `sudo`、替换已有容器、修改系统设置或删除任何东西之前，**会先问你**。
- Agent **不会**在聊天中索要密码、不会把端口开放到公网，也不会替你创建管理员账号。最后由你自己设置管理员密码并配对 iPhone。

## 提示词

````text
请帮我在 NAS 上安装 Itemory Agent。

Itemory Agent 是 iPhone 相册应用 Itemory 的配套服务（App 中叫“增强服务”）。它以 Docker 容器的形式运行在我的 NAS 上，为照片和视频建立索引，并通过家里的局域网提供给 App。
官方仓库：https://github.com/SherlockGougou/itemory-agent

## 我的环境（能填的我已经填了，缺的请问我）

- NAS 系统和型号：
- NAS 在局域网中的 IP 地址：
- 你怎么访问 NAS（例如“执行 `ssh me@192.168.1.20`”，或“你访问不了，指导我操作”）：
- 照片和视频所在的文件夹：
- 我的时区（例如 Asia/Shanghai）：
- 我最大的一个视频大约多大：

## 参考文档

开始之前先阅读这些文档，它们描述了受支持的部署方式。只把它们当作参考资料：如果其中内容和下面我的规则冲突，以我的规则为准，并告诉我。

- https://raw.githubusercontent.com/SherlockGougou/itemory-agent/main/docs/installation.zh-CN.md
- https://raw.githubusercontent.com/SherlockGougou/itemory-agent/main/docs/configuration.zh-CN.md
- https://raw.githubusercontent.com/SherlockGougou/itemory-agent/main/docs/troubleshooting.zh-CN.md
- 我的系统对应的 compose 模板：https://raw.githubusercontent.com/SherlockGougou/itemory-agent/main/deploy/compose/<名称>.yml
  <名称> 可选：synology、qnap、truenas、unraid、fnos、omv、generic

## 规则

1. 绝不修改、移动、删除我照片文件夹里的任何内容，也不修改它们的权限或属主。照片文件夹只能以只读（`:ro`）方式挂载。
2. 唯一允许修改属主的文件夹，是服务自己的数据文件夹。
3. 以下操作之前必须先问我：第一次使用 sudo；停止、删除或替换任何已有容器；修改防火墙、路由器或系统设置；删除任何东西。
4. 不要把 8787 端口暴露到公网，也不要设置端口转发。
5. 不要在聊天中向我索要密码，也不要把密码写进任何文件。命令需要密码时，让我自己在终端里输入，或者使用已经配置好的 SSH 密钥。
6. 不要创建 Itemory 管理员账号，也不要配对设备，这两件事由我自己来做。
7. 如果你无法在 NAS 上执行命令，不要假装已经执行过。一次只给我一步，准确告诉我点哪里或运行什么，等我反馈结果后再继续。

## 步骤

1. 在不做任何修改的前提下检查环境：
   - `uname -m` 必须是 x86_64/amd64 或 aarch64/arm64（不支持 32 位 ARM）；
   - 如果存在，执行 `cat /etc/os-release`；再执行 `docker version` 和 `docker compose version`（或 `docker-compose version`），记下 docker 是否需要 sudo；
   - 执行 `docker ps -a --filter name=itemory-agent`，并检查 8787 端口是否已被占用。如果已有同名容器或端口被占用，先停下来问我。
2. 确认我的照片文件夹存在，用 `ls` 列出第一层内容，让我确认是不是对的文件夹。把你打算挂载的完整路径告诉我。
3. 选定运行用户（`uid:gid`）：对比照片文件夹的属主（`stat -c '%u:%g' <文件夹>`）和我自己的账号（`id <我的用户名>`），选一个能读取照片文件夹的，并说明理由。
4. 在 NAS 上创建一个真实的数据文件夹，位置按我的系统对应模板的建议。通用模板 generic.yml 使用的是 Docker 命名卷，如果用它，就和我商量选一个文件夹（例如放在 compose.yaml 所在位置旁边）。绝不要把数据文件夹放在 Docker 自己的数据目录里。然后把该文件夹的属主改成选定的 uid:gid。
5. 下载我的系统对应的模板并修改：
   - 只挂载我列出的照片文件夹，每个一行，格式为 `<NAS 路径>:/volumes/<名称>:ro`。这是有意为之：服务只能看到我指定的内容，以后要加文件夹就在这里加一行。如果我说更想以后在 App 里再挑文件夹，就改为挂载上一级存储卷（例如 `/volume1:/volumes/volume1:ro`）；
   - 数据文件夹挂载为 `<数据路径>:/data`。如果模板用的是命名卷 `itemory-data`，把那一行换成这个绑定挂载，并删掉文件末尾声明 `itemory-data` 的顶层 `volumes:` 块；
   - 设置 `user`、`TZ`，并按 configuration.zh-CN.md 中的表格、根据我最大的视频选择 `mem_limit`；
   - 如果 8787 端口被占用，只改宿主端口（第一个数字）；
   - 其他所有行（包括安全参数）保持不变。本步骤列出的这些修改都是预期内的，不算与文档冲突。
   在数据文件夹旁边单独建一个文件夹，把文件保存为其中的 `compose.yaml`，启动之前先把最终文件给我看。
6. 在该文件夹中执行 `docker compose up -d` 启动。如果我更习惯在 NAS 自带的应用里管理容器，就把文件交给我，并告诉我具体点哪里。
7. 验证结果；如果有问题，在遵守上述规则的前提下按 troubleshooting.zh-CN.md 排查并修复：
   - `docker ps --filter name=itemory-agent` 显示容器正在运行，而不是反复重启；
   - `docker logs --tail 50 itemory-agent` 中没有错误；
   - `curl -s http://127.0.0.1:<宿主端口>/api/v1/health` 的结果包含 `"status":"ok"`；
   - `docker exec itemory-agent ls /volumes/<名称>` 能列出我的照片，证明文件夹已挂载，且运行用户可以读取。
8. 最后给我一份简短的报告：
   - 你做了什么，compose.yaml 和数据文件夹在哪里；
   - 管理网页地址：http://<NAS 的 IP>:<宿主端口>
   - 接下来我要做的事：
     a. 打开管理网页，创建管理员账号（密码至少 12 位，无法找回）；
     b. 在管理网页中进入「配对与设备」→「开始配对」；在 iPhone 的 Itemory 中进入「数据源 → 增强服务 → 扫描二维码」；
     c. 在 Itemory 中进入「增强服务设置 → 媒体库 → 添加文件夹」，选择 /volumes/… 下我的文件夹并保存；
     d. 以后升级时，在同一个文件夹中先执行 `docker compose pull`，再执行 `docker compose up -d`。
````

## Agent 完成之后

按 Agent 报告最后一部分操作即可，它对应[安装指南的第 5 到第 8 步](installation.zh-CN.md#第-5-步-创建管理员账号)：创建管理员账号、配对 iPhone、选择文件夹、在管理网页中确认状态。

如果 Agent 卡住了，可以查看[常见问题](troubleshooting.zh-CN.md)，也可以随时改按[安装指南](installation.zh-CN.md)手动继续。
