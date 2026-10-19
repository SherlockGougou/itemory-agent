#!/usr/bin/env bash
# 整个函数读取完成后再运行，避免下载中断时执行半份安装程序。
# 安装过程按阶段记录到日志文件；失败时脚本末尾会打印日志路径和复制命令。
itemory_online_install() {
    if ! command -v python3 >/dev/null 2>&1; then
        cat >&2 <<'MESSAGE'
需要 Python 3.9 或更新版本，但没有找到 python3。请先安装 Python 3：
- 群晖：套件中心搜索「Python 3」安装；
- 飞牛、绿联、极空间、威联通：应用中心（App Center）搜索 Python 3；
- TrueNAS SCALE：在应用或 jail 中安装 python3；
- 通用 Linux：用系统包管理器安装，例如 apt install python3 或 dnf install python3。
也可以改用文档中的 Docker Compose 安装方式。本次没有修改任何内容，也没有生成日志文件。
MESSAGE
        return 1
    fi
    python3 - "$@" <<'PY'
import argparse
import collections
import contextlib
import datetime
import fcntl
import hashlib
import ipaddress
import io
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tarfile
import threading
import time
import traceback
import urllib.parse
import uuid
from pathlib import Path

REPOSITORY = "ghcr.io/sherlockgougou/itemory-agent"
TCR_REPOSITORY = "ccr.ccs.tencentyun.com/itemory/itemory-agent"
DEFAULT_DOWNLOAD_BASE = "https://itemory-downloads.oss-cn-chengdu.aliyuncs.com/releases/itemory-agent"

# 反馈渠道与安装器版本号；版本号让支持人员知道用户跑的是哪一版脚本。
INSTALLER_VERSION = "2026-10-19"
SUPPORT_EMAIL = "qinglingou@gmail.com"
TOTAL_STAGES = 7
RECORD_DIRECTORY = None

# NAS 上常见 LANG=C 或缺少 locale：docker 输出里有中文路径时，默认按 ASCII 解码会抛
# UnicodeDecodeError，终端也不能打印中文。统一用 UTF-8 并容错替换，保证错误能记录下来。
TEXT = {"text": True, "encoding": "utf-8", "errors": "replace"}


class InstallError(Exception):
    pass


class UserCancelled(InstallError):
    """用户主动取消：不算失败，不打排查块，也不提示发日志。"""


class Logger:
    """同时写控制台和日志文件。

    日志必须能在脚本崩溃时保留已写内容，所以每次写入都 flush；日志写不进去时
    只影响排查，不能让安装失败。日志会包含系统信息和用户输入的路径，但不写
    配对令牌、密码和照片内容。
    """

    def __init__(self):
        self.path = None
        self.handle = None
        self.debug_enabled = False
        self.stage = "启动"

    def open(self, explicit=None):
        now = datetime.datetime.now().astimezone()
        name = "install-" + now.strftime("%Y%m%d-%H%M%S") + "-" + str(os.getpid()) + ".log"
        candidates = []
        if explicit:
            candidates.append(Path(explicit).expanduser())
        else:
            candidates.extend(default_log_paths(name))
        for candidate in candidates:
            try:
                candidate.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                handle = candidate.open("a", encoding="utf-8", buffering=1)
            except OSError:
                continue
            with contextlib.suppress(OSError):
                os.chmod(candidate, 0o600)
            self.path = candidate
            self.handle = handle
            self.claim_ownership(candidate)
            break
        if self.handle is None:
            self.warn("无法写入日志文件，安装继续，但失败时只能提供屏幕上的输出。")
        return self.path

    def claim_ownership(self, path):
        """sudo 执行时把日志交还给原用户，便于复制后发给支持。"""
        if os.geteuid() != 0:
            return
        uid, gid = os.environ.get("SUDO_UID"), os.environ.get("SUDO_GID")
        if not uid or not gid:
            return
        with contextlib.suppress(Exception):
            uid, gid = int(uid), int(gid)
            for target in (path, path.parent, path.parent.parent):
                os.chown(target, uid, gid)

    def write(self, level, message):
        if self.handle is None:
            return
        stamp = datetime.datetime.now().astimezone().strftime("%Y-%m-%d %H:%M:%S")
        with contextlib.suppress(OSError):
            for line in str(message).splitlines() or [""]:
                self.handle.write(stamp + " [" + level + "] [" + self.stage + "] " + line + "\n")
            self.handle.flush()

    def close(self):
        with contextlib.suppress(OSError):
            if self.handle is not None:
                self.handle.flush()
                self.handle.close()
        self.handle = None

    def banner(self):
        print("Itemory 私有云安装器 " + INSTALLER_VERSION, flush=True)
        print("  每个步骤都会写入日志；如果失败，脚本会打印日志路径和复制命令。", flush=True)
        if self.path:
            print("  日志文件：" + str(self.path), flush=True)
        else:
            print("  日志文件：未能写入（见下方提示）", flush=True)
        print("", flush=True)
        self.write("INFO", "安装器版本：" + INSTALLER_VERSION)
        self.write("INFO", "开始时间：" + datetime.datetime.now().astimezone().isoformat())
        self.write("INFO", "命令行：" + " ".join(sys.argv))
        self.write("INFO", "运行身份：uid=" + str(os.geteuid()) + " gid=" + str(os.getegid())
                   + (" sudo=" + os.environ.get("SUDO_USER", "?") if os.geteuid() == 0 else ""))
        self.write("NOTE", "日志包含系统信息、安装参数和路径，不包含密码、配对令牌或照片内容。")

    def stage_start(self, number, title):
        self.stage = title
        print("", flush=True)
        print("[" + str(number) + "/" + str(TOTAL_STAGES) + "] " + title, flush=True)
        self.write("STEP", "第 " + str(number) + "/" + str(TOTAL_STAGES) + " 步：" + title)

    def info(self, message):
        print("  " + message, flush=True)
        self.write("INFO", message)

    def done(self, message):
        print("  ✓ " + message, flush=True)
        self.write("OK", message)

    def warn(self, message):
        print("  ! " + message, flush=True)
        self.write("WARN", message)

    def error(self, message):
        print("  ✗ " + message, file=sys.stderr, flush=True)
        self.write("ERROR", message)

    def debug(self, message):
        """详细内容始终写文件；加 --debug 时才显示在屏幕上。"""
        if self.debug_enabled:
            print("  · " + message, flush=True)
        self.write("DEBUG", message)

    def section(self, title):
        """把长段输出分成可扫读的小节。"""
        print("", flush=True)
        print("  " + title, flush=True)
        self.write("SECTION", title)


LOG = Logger()


def default_log_paths(name):
    homes = []
    if os.geteuid() == 0 and os.environ.get("SUDO_USER"):
        uid = os.environ.get("SUDO_UID")
        if uid:
            try:
                import pwd
                homes.append(Path(pwd.getpwuid(int(uid)).pw_dir))
            except (KeyError, ValueError):
                pass
    if os.environ.get("HOME"):
        homes.append(Path(os.environ["HOME"]))
    paths = [home / ".cache" / "itemory-agent" / "logs" / name for home in homes]
    paths.append(Path("/tmp") / "itemory-agent-logs" / name)
    return paths


def redact(text):
    """尽力遮住可能出现在命令输出里的凭据，避免日志被转发时泄露。

    HTTP 请求头常用「Authorization: Bearer <token>」这种空格分隔的写法，环境变量和
    JSON 又是「token=…」或「"token": "…"」，两种都要覆盖。
    """
    if not text:
        return text
    patterns = [
        r"(?i)\b((?:authorization|proxy-authorization)\s*[:=]\s*)(?:bearer|basic|token)?\s*\S+",
        r'(?i)\b((?:token|password|passwd|secret|hash|api[-_]?key)[\w.-]{0,20}\s*[:=]\s*)"[^"]*"',
        r"(?i)\b((?:token|password|passwd|secret|hash|api[-_]?key)[\w.-]{0,20}\s*[:=]\s*)\S+",
    ]
    for pattern in patterns:
        text = re.sub(pattern, r"\1***", text)
    return text


def tail(text, limit=2000):
    """保留结尾：命令失败时最有用的信息通常在最后几行。"""
    text = redact((text or "").strip())
    return text if len(text) <= limit else "…（前略 " + str(len(text) - limit) + " 字符）\n" + text[-limit:]


def last_meaningful(lines, limit=120):
    """从命令输出里挑一行能说明当前进度的内容。

    docker pull / load 的进度用 \r 覆盖同一行，按 \n 切开后一段里会有很多片段，
    取最后一段才是最新进度。
    """
    for line in reversed(lines):
        for segment in reversed(line.replace("\r", "\n").splitlines()):
            text = segment.strip()
            if not text or text.startswith("#"):
                continue
            return text if len(text) <= limit else text[:limit - 1] + "…"
    return ""


def describe(args):
    parts = []
    for item in args:
        text = str(item)
        parts.append(text if len(text) <= 160 else text[:157] + "…")
    return " ".join(parts)


def stream(args):
    """执行会产生大量进度的命令（pull / load / up），把输出收进日志并显示心跳。"""
    LOG.debug("执行：" + describe(args))
    process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=1, **TEXT)
    lines = collections.deque(maxlen=200)

    def pump():
        for line in process.stdout:
            line = line.rstrip("\n")
            if not line:
                continue
            lines.append(line)
            if LOG.debug_enabled:
                print("  · " + line, flush=True)

    reader = threading.Thread(target=pump, daemon=True)
    reader.start()
    started = time.monotonic()
    next_note = 15
    while process.poll() is None:
        time.sleep(1)
        elapsed = time.monotonic() - started
        if elapsed >= next_note:
            latest = last_meaningful(list(lines))
            print("  · 已进行 " + str(int(elapsed)) + " 秒" + ("：" + latest if latest else "…"), flush=True)
            next_note += 15
    reader.join(timeout=5)
    for line in list(lines):
        LOG.write("OUTPUT", redact(line))
    if process.returncode:
        output = "\n".join(list(lines)[-20:])
        if output:
            LOG.error("命令最后输出：\n" + redact(output))
        raise InstallError("命令失败（退出码 " + str(process.returncode) + "）：" + describe(args[:4])
                           + ("\n" + redact(output) if output else ""))
    return "\n".join(list(lines))


def run(args, capture=True, stream_output=False, log_output=True, **kwargs):
    LOG.debug("执行：" + describe(args))
    if stream_output:
        return stream(args)
    started = time.monotonic()
    result = subprocess.run(args, stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.PIPE if capture else None, **TEXT, **kwargs)
    elapsed = time.monotonic() - started
    if capture and log_output:
        if result.stdout and result.stdout.strip():
            LOG.debug("输出（" + str(round(elapsed, 1)) + " 秒）：" + tail(result.stdout))
        if result.stderr and result.stderr.strip():
            LOG.debug("错误输出：" + tail(result.stderr))
    if result.returncode:
        # 不打印可能含凭据的 Compose 内容或完整命令。
        raise InstallError("命令失败（退出码 " + str(result.returncode) + "）：" + describe(args[:4]) + "\n"
                           + (tail(result.stderr) or "请查看上方错误。"))
    return result.stdout.strip() if capture else ""


def docker(*args, **kwargs):
    return run(["docker", *args], **kwargs)


def inspect(container):
    # inspect 里有环境变量等可能敏感的内容，只进内存，不写日志。
    return json.loads(docker("inspect", container, log_output=False))[0]


def container_logs(container, lines=120):
    result = subprocess.run(["docker", "logs", "--tail", str(lines), container],
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, **TEXT)
    return redact((result.stdout or "").strip())


def ask(label, default=""):
    prompt = label + (" [" + default + "]" if default else "") + ": "
    try:
        with open("/dev/tty", "w") as terminal:
            terminal.write(prompt)
            terminal.flush()
        with open("/dev/tty", "r") as terminal:
            line = terminal.readline()
    except OSError as exc:
        raise InstallError("没有交互终端，请通过参数提供配置并使用 --yes。") from exc
    if not line:
        raise InstallError("交互输入已关闭。")
    answer = line.strip() or default
    LOG.write("INPUT", prompt + answer)
    return answer


def choose(label, options, default):
    """显示编号选项，返回选项值；同时兼容旧输入，避免重复执行时卡住。"""
    valid = {str(index): value for index, (value, _) in enumerate(options, 1)}
    aliases = {value.lower(): value for value, _ in options}
    default_index = next((str(index) for index, (value, _) in enumerate(options, 1)
                          if value == default), "1")
    print("", flush=True)
    for index, (_, description) in enumerate(options, 1):
        suffix = "（默认）" if str(index) == default_index else ""
        print("  " + str(index) + ". " + description + suffix, flush=True)
    while True:
        answer = ask(label, default_index).strip().lower()
        if answer in valid:
            return valid[answer]
        if answer in aliases:
            return aliases[answer]
        print("  请输入选项编号：1 或 2。", flush=True)


def confirm(args, message):
    LOG.section("需要确认")
    LOG.info(message)
    LOG.write("INFO", "需确认：" + message)
    if not args.yes:
        answer = choose("请选择", [("yes", "继续"), ("no", "取消")], "no")
        if answer != "yes":
            raise UserCancelled("已取消，服务未变更。")
    LOG.write("INFO", "用户已确认")


def write_json(path, value):
    with path.open("x", encoding="utf-8") as output:
        json.dump(value, output, ensure_ascii=False, indent=2)
        output.write("\n")


def compose_escape(value):
    # 仅用于新生成的配置；Compose config 输出本身已转义 $，不能再次转义。
    if isinstance(value, str):
        return value.replace("$", "$$")
    if isinstance(value, list):
        return [compose_escape(item) for item in value]
    if isinstance(value, dict):
        return {key: compose_escape(item) for key, item in value.items()}
    return value


def compose_command(files, project, directory):
    command = ["docker", "compose", "--project-directory", directory, "-p", project]
    for file in files:
        command.extend(["-f", str(file)])
    return command


def validate_version(version):
    if not isinstance(version, str) or not re.fullmatch(r"[0-9]+(?:\.[0-9]+\.[0-9]+(?:[-.][A-Za-z0-9.-]+)?)?", version):
        raise InstallError("--version 需要镜像版本号，例如 0.5.0。")


def resolve_image(version, repository=REPOSITORY):
    validate_version(version)
    if repository not in (REPOSITORY, TCR_REPOSITORY):
        raise InstallError("只支持已配置的官方镜像源。")
    reference = repository + ":" + version
    LOG.info("正在拉取镜像 " + reference + "（第一次可能需要几分钟）…")
    docker("pull", reference, stream_output=True)
    LOG.done("镜像下载完成")
    image = json.loads(docker("image", "inspect", reference, log_output=False))
    if not image:
        raise InstallError("拉取后找不到镜像 " + reference + "。")
    image = image[0]
    digests = [item for item in image.get("RepoDigests", []) if item.startswith(repository + "@sha256:")]
    if not digests:
        raise InstallError("镜像没有可验证的仓库 digest，安装已停止。")
    LOG.info("镜像摘要：" + digests[0])
    return digests[0]


def download(url, destination, max_bytes=None):
    # curl 负责 TLS、超时与进度；签名链接可能含凭据，不回显完整 URL。
    # 日志只记协议、主机和路径，不记查询参数。
    LOG.info("下载：" + redact(urllib.parse.urlsplit(url).netloc + urllib.parse.urlsplit(url).path)
             + ("（上限 " + str(round(max_bytes / 1024**2)) + " MiB）" if max_bytes else ""))
    result = subprocess.run(["curl", "--fail", "--location", "--proto", "=https", "--proto-redir", "=https",
                             "--connect-timeout", "20", "--max-time", "1800", "--retry", "2",
                             "--speed-limit", "1024", "--speed-time", "60", "--output", str(destination),
                             *(["--max-filesize", str(max_bytes)] if max_bytes else []), url],
                            stderr=subprocess.PIPE, **TEXT)
    if result.returncode:
        LOG.error("curl 错误输出：" + tail(result.stderr))
        raise InstallError("下载失败（curl 错误编号 " + str(result.returncode) + "）。请检查网络、下载地址和访问权限；服务未替换。")
    if max_bytes and destination.stat().st_size > max_bytes:
        raise InstallError("下载内容超过大小限制。")


def resolve_archive_image(args, architecture):
    validate_version(args.version)
    base = args.download_base.rstrip("/")
    parsed = urllib.parse.urlsplit(base)
    if parsed.scheme != "https" or not parsed.netloc or parsed.query or parsed.fragment or parsed.username or parsed.password:
        raise InstallError("--download-base 必须是无凭据、查询参数和 fragment 的 HTTPS 目录地址。")
    cache = safe_directory(args.download_dir, "下载缓存路径")
    cache.mkdir(parents=True, exist_ok=True, mode=0o700)
    manifest_path = cache / ("manifest-" + uuid.uuid4().hex + ".json")
    location = ("channels/" + args.version + ".json" if "." not in args.version else "versions/" + args.version + "/manifest.json")
    try:
        LOG.info("正在获取 OSS 发布清单…")
        download(base + "/" + location, manifest_path, 1024 * 1024)
        manifest = json.loads(manifest_path.read_text())
    finally:
        manifest_path.unlink(missing_ok=True)
    if not isinstance(manifest, dict):
        raise InstallError("OSS 发布清单必须是 JSON 对象。")
    version = manifest.get("version", "")
    validate_version(version)
    if "." not in version or manifest.get("schema") != 1 or manifest.get("repository") != REPOSITORY:
        raise InstallError("OSS 发布清单格式或镜像仓库不正确。")
    if "." in args.version and version != args.version:
        raise InstallError("发布清单版本与指定版本不一致。")
    if "." not in args.version and args.version != ("1" if version.split(".")[0] == "0" else version.split(".")[0]):
        raise InstallError("发布清单版本与指定通道不一致。")
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(architecture, architecture)
    images = manifest.get("images")
    if not isinstance(images, dict):
        raise InstallError("发布清单缺少镜像列表。")
    entry = images.get(arch)
    if not isinstance(entry, dict):
        raise InstallError("发布清单缺少当前架构：" + arch)
    filename = "itemory-agent-" + version + "-" + arch + ".tar.gz"
    checksum, image_id, size = entry.get("sha256", ""), entry.get("image_id", ""), entry.get("size")
    if (entry.get("file") != filename or not isinstance(checksum, str) or not re.fullmatch(r"[a-f0-9]{64}", checksum)
            or not isinstance(image_id, str) or not re.fullmatch(r"sha256:[a-f0-9]{64}", image_id)
            or type(size) is not int or not 0 < size <= 8 * 1024**3):
        raise InstallError("发布清单中的文件名、校验和、镜像 ID 或大小不正确。")
    LOG.info("发布清单校验通过：版本 " + version + "，架构 " + arch)
    archive = cache / (checksum + ".tar.gz")
    def valid_archive():
        if not archive.is_file() or archive.stat().st_size != size:
            return False
        digest = hashlib.sha256()
        with archive.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        return digest.hexdigest() == checksum
    if not valid_archive():
        partial = cache / (checksum + "." + uuid.uuid4().hex + ".part")
        try:
            LOG.info("正在下载 " + filename + "（" + str(round(size / 1024**2)) + " MiB）…")
            download(base + "/versions/" + version + "/" + filename, partial, size)
            # 下载文件先校验，再交给 Docker；失败不导入，更不会停止旧服务。
            digest = hashlib.sha256()
            with partial.open("rb") as source:
                for chunk in iter(lambda: source.read(1024 * 1024), b""):
                    digest.update(chunk)
            if partial.stat().st_size != size or digest.hexdigest() != checksum:
                raise InstallError("OSS 镜像包大小或 SHA-256 校验失败，未导入镜像。")
            partial.replace(archive)
        finally:
            partial.unlink(missing_ok=True)
        LOG.done("镜像包大小与 SHA-256 校验通过")
    else:
        LOG.info("命中已校验的下载缓存：" + str(archive))
    LOG.info("正在导入 Docker 镜像…")
    docker("image", "load", "--input", str(archive), stream_output=True)
    images = json.loads(docker("image", "inspect", image_id, log_output=False))
    if not images:
        raise InstallError("导入后找不到镜像 " + image_id + "。")
    image = images[0]
    if image.get("Id") != image_id or image.get("Os") != "linux" or image.get("Architecture") != arch:
        raise InstallError("导入镜像的 ID 或平台与发布清单不一致。")
    if (image.get("Config", {}).get("Labels") or {}).get("org.opencontainers.image.source") != "https://github.com/SherlockGougou/itemory-agent":
        raise InstallError("导入镜像缺少官方来源标记。")
    LOG.done("镜像已导入并核对平台与官方来源标记")
    # docker save/load 不保证保留 RepoDigests，使用内容寻址的本地 Image ID 固定镜像。
    return image_id


def data_mount(container):
    mounts = [item for item in container["Mounts"] if item["Destination"] == "/data"]
    if len(mounts) != 1 or mounts[0]["Type"] not in ("bind", "volume") or not mounts[0]["RW"]:
        raise InstallError("现有服务必须有独立、可写的 /data 挂载；不会自动迁移未知数据布局。")
    env = dict(item.split("=", 1) for item in container["Config"].get("Env", []) if "=" in item)
    if env.get("ITEMORY_CACHE_DIR", "/data") != "/data":
        raise InstallError("现有服务的数据目录不是 /data，请先按安装文档整理数据挂载。")
    return mounts[0]


def old_configuration(container):
    labels = container["Config"].get("Labels") or {}
    project = labels.get("com.docker.compose.project")
    service = labels.get("com.docker.compose.service")
    directory = labels.get("com.docker.compose.project.working_dir")
    files = labels.get("com.docker.compose.project.config_files", "").split(",")
    if not all([project, service, directory, files[0]]) or not all(Path(item).is_file() for item in files):
        raise InstallError("检测到已有容器，但找不到完整 Compose 源配置。"
                           "为保留挂载、权限和配对，安装器没有停止服务；请恢复原配置后重试。")
    LOG.info("原 Compose 项目：" + project + "，服务：" + service + "，目录：" + directory)
    command = compose_command(files, project, directory)
    model = json.loads(run(command + ["config", "--format", "json"], log_output=False))
    expected = run(command + ["config", "--hash", service]).split()
    if len(expected) != 2 or expected[1] != labels.get("com.docker.compose.config-hash"):
        raise InstallError("Compose 配置或 Compose 版本与创建现有容器时不一致。请用原配置和原 Compose 版本核对并同步运行状态后重试；未停止服务。")
    data_mount(container)
    return model, project, service, directory


def legacy_configuration(container):
    """把旧式 docker run 容器转换成可接管的 Compose 服务。

    旧安装没有 Compose labels，不能靠 labels 还原配置。这里只迁移 Docker
    inspect 能明确读出的运行时参数；遇到无法确定语义的配置时停止，避免用
    猜测的配置替换正在服务的容器。
    """
    config = container.get("Config") or {}
    host = container.get("HostConfig") or {}
    name = container["Name"].lstrip("/")
    data = data_mount(container)
    entrypoint = config.get("Entrypoint")
    if (entrypoint not in (None, [])
            and not (isinstance(entrypoint, list) and len(entrypoint) == 1
                     and Path(str(entrypoint[0])).name == "itemory-agent")):
        raise InstallError("旧容器自定义了 entrypoint，安装器无法确认启动方式；未进行变更。")
    if config.get("Cmd") not in (None, []):
        raise InstallError("旧容器自定义了 command，安装器无法确认启动方式；未进行变更。")
    if config.get("WorkingDir") not in (None, "", "/"):
        raise InstallError("旧容器自定义了 working_dir，安装器无法确认工作目录；未进行变更。")
    if host.get("NetworkMode") not in (None, "", "default", "bridge"):
        raise InstallError("旧容器使用非默认网络模式（" + str(host["NetworkMode"])
                           + "），安装器不会自动迁移；未进行变更。")
    if host.get("CapAdd"):
        raise InstallError("旧容器额外添加了 Linux capability，安装器不会自动迁移；未进行变更。")
    if host.get("Devices"):
        raise InstallError("旧容器映射了设备，安装器不会自动迁移；未进行变更。")
    if host.get("ExtraHosts"):
        raise InstallError("旧容器配置了额外 hosts，安装器不会自动迁移；未进行变更。")
    if host.get("Dns") or host.get("DnsOptions") or host.get("DnsSearch"):
        raise InstallError("旧容器自定义了 DNS，安装器不会自动迁移；未进行变更。")
    if (host.get("Links") or host.get("PidMode") or host.get("UTSMode")
            or host.get("IpcMode") not in (None, "", "private")):
        raise InstallError("旧容器使用安装器不迁移的命名空间或链接配置；未进行变更。")
    if host.get("Privileged"):
        raise InstallError("旧容器启用了 privileged，安装器不会自动迁移；未进行变更。")
    if host.get("LogConfig", {}).get("Type") not in (None, "", "json-file", "local"):
        raise InstallError("旧容器使用非默认日志驱动，安装器不会自动迁移；未进行变更。")

    volumes = []
    named_volumes = {}
    for mount in container.get("Mounts") or []:
        target = mount.get("Destination")
        source = mount.get("Source")
        if mount.get("Type") not in ("bind", "volume") or not target or not source:
            raise InstallError("旧容器存在无法迁移的挂载：" + str(target or "未知路径") + "；未进行变更。")
        item = {"type": mount["Type"], "source": source, "target": target}
        if not mount.get("RW"):
            item["read_only"] = True
        if mount["Type"] == "bind":
            item["bind"] = {"create_host_path": False}
        elif mount.get("Name"):
            # 保留原命名卷，不能让它变成新 Compose 项目下的另一个卷。
            item["source"] = mount["Name"]
            named_volumes[mount["Name"]] = {"name": mount["Name"], "external": True}
        else:
            raise InstallError("旧容器使用了无法识别的匿名卷：" + str(target) + "；未进行变更。")
        volumes.append(item)

    ports = []
    actual_ports = (container.get("NetworkSettings") or {}).get("Ports") or host.get("PortBindings") or {}
    for target, bindings in sorted(actual_ports.items()):
        if "/" not in target:
            raise InstallError("旧容器端口格式无法识别：" + str(target) + "；未进行变更。")
        port, protocol = target.split("/", 1)
        for binding in bindings or []:
            item = {"target": int(port), "protocol": protocol}
            if binding.get("HostIp"):
                item["host_ip"] = binding["HostIp"]
            if binding.get("HostPort"):
                item["published"] = binding["HostPort"]
            if item.get("published"):
                ports.append(item)

    service = {"image": config.get("Image") or container.get("Image"),
               "container_name": name, "restart": host.get("RestartPolicy", {}).get("Name") or "no",
               "volumes": volumes}
    if config.get("User"):
        service["user"] = config["User"]
    if host.get("ReadonlyRootfs"):
        service["read_only"] = True
    if host.get("Tmpfs"):
        service["tmpfs"] = [key + (":" + value if value else "") for key, value in sorted(host["Tmpfs"].items())]
    if host.get("CapDrop"):
        service["cap_drop"] = host["CapDrop"]
    if host.get("SecurityOpt"):
        service["security_opt"] = host["SecurityOpt"]
    if ports:
        service["ports"] = ports
    environment = {}
    for item in config.get("Env") or []:
        if "=" in item:
            key, value = item.split("=", 1)
            environment[key] = value
    if environment:
        service["environment"] = environment
    if host.get("Memory"):
        service["mem_limit"] = str(host["Memory"])
    if host.get("NanoCpus"):
        service["cpus"] = str(host["NanoCpus"] / 1_000_000_000)
    if host.get("StopTimeout"):
        service["stop_grace_period"] = str(host["StopTimeout"]) + "s"

    project = "itemory-" + uuid.uuid5(uuid.NAMESPACE_URL, "legacy:" + container["Id"]).hex[:12]
    if data["Type"] == "bind":
        directory = data["Source"].rsplit("/", 1)[0] or "/"
    else:
        directory = "/var/lib/itemory-agent"
    if directory == "/":
        raise InstallError("旧容器的 /data 位于根目录，无法确定安全的 Compose 项目目录；未进行变更。")
    model = {"services": {"itemory-agent": service}}
    if named_volumes:
        model["volumes"] = named_volumes
    return model, project, "itemory-agent", directory


def health(container, timeout=120):
    LOG.info("等待服务通过健康检查（最多 " + str(timeout) + " 秒）…")
    deadline = time.monotonic() + timeout
    started = time.monotonic()
    attempt = 0
    while time.monotonic() < deadline:
        attempt += 1
        state = inspect(container)["State"]
        status = state.get("Status")
        if status in ("exited", "dead"):
            logs = container_logs(container)
            LOG.error("服务启动后退出（状态 " + str(status) + "）。容器最近的日志：\n" + (logs or "（无输出）"))
            raise InstallError("服务启动后退出。容器最近的输出已写入安装日志。")
        result = subprocess.run(["docker", "exec", container, "itemory-agent", "healthcheck"],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if state.get("Running") and result.returncode == 0:
            LOG.done("健康检查通过（第 " + str(attempt) + " 次尝试）")
            return
        LOG.write("DEBUG", "健康检查第 " + str(attempt) + " 次未通过：退出码 " + str(result.returncode)
                  + "，状态 " + str(status))
        if attempt % 10 == 0:
            LOG.info("服务仍在启动中，已等待 " + str(int(time.monotonic() - started)) + " 秒…")
        time.sleep(2)
    logs = container_logs(container)
    LOG.error("健康检查超时。容器最近的日志：\n" + (logs or "（无输出）"))
    raise InstallError("服务在 " + str(timeout) + " 秒内未通过健康检查，容器日志已写入安装日志。")


def identity_from_archive(archive):
    result = {}
    with tarfile.open(archive, "r:") as backup:
        for name in ("tokens.json", "admin.json", "settings.json"):
            try:
                member = backup.getmember("./" + name)
            except KeyError:
                continue
            if not member.isfile() or member.size > 16 * 1024 * 1024:
                raise InstallError("备份中的身份或设置文件异常：" + name)
            with backup.extractfile(member) as source:
                result[name] = json.load(source)
    LOG.debug("备份中包含的身份文件：" + (", ".join(sorted(result)) or "无"))
    return result


def verify_identity(container, before):
    for name, previous in before.items():
        # 内容只用于比对，绝不写进日志。
        current = json.loads(docker("exec", container, "cat", "/data/" + name, log_output=False))
        if name == "tokens.json":
            if current.get("serverId") != previous.get("serverId"):
                raise InstallError("升级后 serverId 发生变化。")
            tokens = {item["id"]: item["hash"] for item in current.get("tokens", [])}
            if any(tokens.get(item["id"]) != item["hash"] for item in previous.get("tokens", [])):
                raise InstallError("升级后配对设备发生丢失或令牌变化。")
            LOG.done("serverId 与 " + str(len(previous.get("tokens", []))) + " 个已配对设备核对通过")
        elif name == "admin.json" and current != previous:
            raise InstallError("升级后管理员凭据发生变化。")
        elif name == "settings.json":
            # 服务可能为新版本补齐默认字段，但已有字段不能被安装器覆盖。
            if any(current.get(key) != value for key, value in previous.items()):
                raise InstallError("升级后已有设置发生变化，请检查备份与版本迁移。")
            LOG.done("已有设置保持不变")


def safe_directory(path, label):
    if not path or not Path(path).expanduser().is_absolute():
        raise InstallError(label + "必须是非空的绝对路径。")
    path = Path(path).expanduser().resolve()
    if not path.is_absolute() or str(path) == "/" or any(char in str(path) for char in ("\n", "\r", ",")):
        raise InstallError(label + "不是安全的独立目录。")
    return path


def check_record_directory(root, container):
    mount = data_mount(container)
    if mount["Type"] == "bind":
        data = Path(mount["Source"]).resolve()
        if root == data or data in root.parents:
            raise InstallError("备份目录位于 /data 内，已停止以避免递归备份。")
    # 整卷挂载很常见；判断实际媒体库，而非把整个挂载卷都当成照片目录。
    copied = subprocess.run(["docker", "cp", container["Id"] + ":/data/settings.json", "-"],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if copied.returncode:
        raise InstallError("无法读取原设置以检查备份位置，未停止服务。")
    with tarfile.open(fileobj=io.BytesIO(copied.stdout)) as archive:
        members = [item for item in archive.getmembers() if item.name.endswith("settings.json") and item.isfile()]
        if len(members) != 1 or members[0].size > 16 * 1024 * 1024:
            raise InstallError("原 settings.json 布局异常。")
        with archive.extractfile(members[0]) as source:
            libraries = json.load(source).get("libraries", [])
    for library in libraries:
        library_path = Path(library["path"])
        matches = [item for item in container["Mounts"] if item["Type"] == "bind" and
                   (Path(item["Destination"]) == library_path or Path(item["Destination"]) in library_path.parents)]
        if not matches:
            continue
        item = max(matches, key=lambda value: len(value["Destination"]))
        photo = (Path(item["Source"]) / library_path.relative_to(item["Destination"])).resolve()
        if root == photo or photo in root.parents:
            raise InstallError("安装记录目录位于实际媒体库内，请先将 Compose 项目放到媒体库之外。")
    LOG.done("备份目录位置检查通过")


def describe_container(container):
    """已有容器的配置摘要：只写结构和环境变量名，不写取值。"""
    config = container.get("Config") or {}
    host = container.get("HostConfig") or {}
    lines = ["现有容器配置摘要：",
             "  镜像 = " + str(config.get("Image")),
             "  运行用户 = " + str(config.get("User") or "镜像默认"),
             "  只读根目录 = " + str(host.get("ReadonlyRootfs")),
             "  端口映射 = " + (", ".join(sorted(host.get("PortBindings") or {}))
                              or "无")]
    for mount in container.get("Mounts") or []:
        lines.append("  挂载 " + str(mount.get("Source")) + " -> " + str(mount.get("Destination"))
                     + ("（只读）" if not mount.get("RW") else ""))
    environment = [item.split("=", 1)[0] for item in config.get("Env") or [] if "=" in item]
    if environment:
        lines.append("  环境变量名：" + ", ".join(sorted(environment)))
    return "\n".join(lines)


def describe_service(service):
    """记录配置摘要：只写环境变量的名字，不写取值。"""
    lines = ["服务配置摘要："]
    for key in ("image", "container_name", "user", "restart", "read_only", "cpus", "mem_limit", "stop_grace_period"):
        if service.get(key) is not None:
            lines.append("  " + key + " = " + str(service[key]))
    for volume in service.get("volumes") or []:
        if isinstance(volume, dict):
            lines.append("  挂载 " + str(volume.get("source", "?")) + " -> " + str(volume.get("target", "?"))
                         + ("（只读）" if volume.get("read_only") else ""))
        else:
            lines.append("  挂载 " + str(volume))
    for port in service.get("ports") or []:
        if isinstance(port, dict):
            lines.append("  端口 " + str(port.get("published", "?")) + " -> " + str(port.get("target", "?")))
        else:
            lines.append("  端口 " + str(port))
    environment = service.get("environment") or {}
    if isinstance(environment, dict) and environment:
        lines.append("  环境变量名：" + ", ".join(sorted(environment)))
    return "\n".join(lines)


def display_ports(ports):
    """合并 IPv4/IPv6 的同一宿主端口，避免迁移提示重复。"""
    seen = set()
    result = []
    for item in ports or []:
        key = (str(item.get("published", "?")), str(item.get("target", "?")),
               str(item.get("protocol", "tcp")))
        if key in seen:
            continue
        seen.add(key)
        result.append(key)
    return ", ".join(published + " -> " + target for published, target, _ in result)


def management_urls(ports):
    """同一宿主端口只输出一个管理地址，优先使用通配地址。"""
    by_port = {}
    for binding in (ports or {}).get("8787/tcp") or []:
        host = binding.get("HostIp", "")
        published = str(binding.get("HostPort", ""))
        if not published:
            continue
        if host in ("0.0.0.0", "::", ""):
            by_port[published] = "<NAS局域网IP>"
        else:
            by_port.setdefault(published, host)
    urls = []
    for published, host in sorted(by_port.items()):
        if host not in ("0.0.0.0", "::", "<NAS局域网IP>"):
            try:
                if ipaddress.ip_address(host).version == 6:
                    host = "[" + host + "]"
            except ValueError:
                pass
        urls.append("http://" + host + ":" + published)
    return urls


def choose_legacy_migration(args, container, model):
    """解释旧安装为什么不能直接升级，并让用户明确选择迁移或退出。"""
    name = container["Name"].lstrip("/")
    service = model["services"]["itemory-agent"]
    LOG.section("检测结果")
    LOG.info("容器：" + name + "（" + container["Id"][:12] + "）")
    LOG.info("镜像：" + str(service.get("image")))
    LOG.info("状态：运行中，但由旧版安装方式创建，没有 Compose 配置。")
    LOG.info("这不是镜像下载失败；安装器尚未修改任何内容。")

    LOG.section("当前配置")
    for volume in service.get("volumes") or []:
        label = "数据" if volume.get("target") == "/data" else "照片"
        LOG.info(label + "：" + str(volume.get("source")) + " -> "
                 + str(volume.get("target")) + ("（只读）" if volume.get("read_only") else ""))
    ports = display_ports(service.get("ports"))
    LOG.info("运行用户：" + str(service.get("user") or "镜像默认")
             + "；端口：" + (ports or "无"))

    LOG.section("迁移会做什么")
    LOG.info("1. 停止旧容器，并完整备份 /data。")
    LOG.info("2. 旧容器改名保留，作为回滚点，不会删除。")
    LOG.info("3. 用官方镜像启动 Compose 服务。")
    LOG.info("4. 保留照片挂载、运行用户、端口和资源限制。")
    LOG.info("5. 核对健康状态、管理员、serverId 和已配对设备。")

    LOG.section("不会改变")
    LOG.info("照片文件不会移动或删除；手机配对不需要重做。")
    LOG.info("迁移前照片仍只读；已有可写挂载保持原状。")
    LOG.write("INFO", describe_service(service))

    if getattr(args, "migrate_existing", False):
        LOG.write("INFO", "用户通过 --migrate-existing 选择迁移")
        return
    if args.yes:
        raise UserCancelled("检测到旧式 Itemory 安装。--yes 不会自动迁移正在使用的服务；"
                            "请交互运行并选择“迁移并继续”，或显式加 --migrate-existing。未进行变更。")

    LOG.section("请选择")
    answer = choose("请输入选项编号", [("migrate", "迁移并继续"),
                                      ("exit", "退出，不修改现有服务")], "migrate")
    if answer == "migrate":
        LOG.write("INFO", "用户选择迁移旧安装")
        return
    raise UserCancelled("已退出，现有 Itemory 服务未做任何修改。")


def fresh_configuration(args, image):
    photos = args.photo_dir
    if not photos:
        if args.yes:
            raise InstallError("全新安装使用 --yes 时必须提供 --photo-dir。")
        photos = [ask("照片目录的完整路径")]
        while choose("还要添加照片目录吗？", [("yes", "添加另一个目录"),
                                               ("no", "不再添加")], "no") == "yes":
            photos.append(ask("照片目录的完整路径"))
    photos = [safe_directory(item, "照片路径") for item in photos]
    if any(not item.is_dir() for item in photos):
        raise InstallError("照片目录不存在。")
    if len(set(photos)) != len(photos):
        raise InstallError("照片目录不能重复。")
    default_dir = str(photos[0].parent / "itemory-agent")
    root = safe_directory(args.install_dir or (default_dir if args.yes else ask("安装与数据目录（不要放在 Docker 自身数据目录内）", default_dir)), "安装路径")
    for photo in photos:
        if root == photo or photo in root.parents or root in photo.parents:
            raise InstallError("安装与照片目录不能互相包含，避免备份或缓存被索引。")
    if root.exists() and any(root.iterdir()):
        raise InstallError("安装目录不是空目录，且未检测到对应容器；不会覆盖已有配置或数据。")
    owner = os.stat(photos[0])
    default_user = str(owner.st_uid or int(os.environ.get("SUDO_UID", "1000"))) + ":" + str(owner.st_gid)
    user = args.user or (default_user if args.yes else ask("容器运行 uid:gid", default_user))
    if not re.fullmatch(r"[1-9][0-9]*:[0-9]+", user):
        raise InstallError("运行用户必须是非 root 的数字 uid:gid。")
    port = args.port or (8787 if args.yes else int(ask("服务端口", "8787")))
    if not 1024 <= port <= 65535:
        raise InstallError("服务端口必须在 1024 到 65535 之间。")
    with socket.socket() as probe:
        probe.bind(("0.0.0.0", port))
    timezone = args.timezone or ("Asia/Shanghai" if args.yes else ask("时区", "Asia/Shanghai"))
    volumes = [{"type": "bind", "source": str(path), "target": "/volumes/photos" + str(index + 1), "read_only": True,
                "bind": {"create_host_path": False}} for index, path in enumerate(photos)]
    volumes.append({"type": "bind", "source": str(root / "data"), "target": "/data", "bind": {"create_host_path": False}})
    service = {"image": image, "container_name": args.container or "itemory-agent", "restart": "unless-stopped", "user": user,
               "read_only": True, "tmpfs": ["/tmp:size=128m"], "cap_drop": ["ALL"],
               "security_opt": ["no-new-privileges:true"], "volumes": volumes,
               "ports": [{"target": 8787, "published": str(port), "protocol": "tcp"}],
               "environment": {"ITEMORY_PRESET": "balanced", "TZ": timezone},
               "mem_limit": "2g", "stop_grace_period": "30s"}
    LOG.info("照片目录（只读）：" + ", ".join(map(str, photos)))
    LOG.info("数据目录：" + str(root / "data") + "；运行用户：" + user + "；端口：" + str(port))
    # 不写 cpus：它是 Docker 的硬上限，静态值大于 1 会让单核 NAS 直接创建失败，
    # 写 1 又会让多核机器慢十几倍。资源占用由 ITEMORY_PRESET 的并发数控制。
    LOG.info("资源占用：" + "balanced 预设（并发 2），内存 2 GiB；时区：" + timezone)
    LOG.info("不设置 CPU 硬上限：需要限速时可在配置里加 cpus，但不能超过这台机器核数")
    confirm(args, "将创建安装目录并启动 Itemory 私有云；不会修改照片目录权限。")
    new_root = not root.exists()
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    data = root / "data"
    data.mkdir(mode=0o750)
    uid, gid = map(int, user.split(":"))
    if os.geteuid() == 0:
        os.chown(data, uid, gid)
    # 使用最终镜像和用户检查挂载，而非仅根据宿主目录属主推断权限。
    probe = ["run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", user]
    for index, photo in enumerate(photos):
        probe += ["--mount", "type=bind,src=" + str(photo) + ",dst=/photos" + str(index) + ",readonly"]
    probe += ["--mount", "type=bind,src=" + str(data) + ",dst=/data", "--entrypoint", "/bin/sh", image,
              "-ec", 'test -w /data; for p in /photos*; do test -r "$p" && test -x "$p"; sample=$(find "$p" -maxdepth 2 -type f -print -quit); if [ -n "$sample" ]; then test -r "$sample"; fi; done']
    LOG.info("正在用最终镜像检查数据目录和照片目录的读写权限…")
    try:
        docker(*probe)
    except BaseException:
        # 只移除本次创建且仍为空的目录，便于修正 uid 后重新安装。
        with contextlib.suppress(OSError):
            data.rmdir()
        if new_root:
            with contextlib.suppress(OSError):
                root.rmdir()
        raise
    LOG.done("权限预检通过：容器用户可写数据目录、可读照片目录")
    project = "itemory-" + uuid.uuid5(uuid.NAMESPACE_URL, str(root)).hex[:12]
    return {"services": {"itemory-agent": service}}, project, "itemory-agent", str(root)


def find_existing(name):
    ids = docker("ps", "-aq").splitlines()
    if not ids:
        return None
    containers = json.loads(docker("inspect", *ids, log_output=False))
    if name:
        matches = [item for item in containers if item["Name"].lstrip("/") == name]
    else:
        matches = [item for item in containers if official_container(item)
                   or item["Name"] == "/itemory-agent"]
    if len(matches) > 1:
        raise InstallError("检测到多个安装，请用 --container 指定目标容器名。")
    if matches and not (official_container(matches[0]) or legacy_itemory_container(matches[0])):
        raise InstallError("目标容器不是 Itemory 安装：镜像为 " + str(matches[0].get("Config", {}).get("Image"))
                           + "。未进行变更。")
    return matches[0] if matches else None


def compose_managed(container):
    labels = (container.get("Config") or {}).get("Labels") or {}
    files = labels.get("com.docker.compose.project.config_files", "").split(",")
    return bool(labels.get("com.docker.compose.project") and labels.get("com.docker.compose.service")
                and labels.get("com.docker.compose.project.working_dir") and files[0])


def legacy_itemory_container(container):
    """识别早期用 docker run 或本地标签创建的 Itemory 容器。"""
    reference = str((container.get("Config") or {}).get("Image") or "")
    image_name = reference.split("@", 1)[0].rsplit("/", 1)[-1].split(":", 1)[0]
    return image_name == "itemory-agent"


def official_container(container):
    reference = container["Config"]["Image"]
    if reference.split("@")[0].split(":")[0] in (REPOSITORY, TCR_REPOSITORY):
        return True
    labels = container["Config"].get("Labels") or {}
    return (reference == container.get("Image") and re.fullmatch(r"sha256:[a-f0-9]{64}", reference) is not None
            and labels.get("org.opencontainers.image.source") == "https://github.com/SherlockGougou/itemory-agent")


def read_os_release():
    with contextlib.suppress(OSError):
        for line in Path("/etc/os-release").read_text(encoding="utf-8", errors="replace").splitlines():
            if line.startswith("PRETTY_NAME="):
                return line.split("=", 1)[1].strip().strip('"')
    return "unknown"


def environment_summary(info, endpoint):
    uname = os.uname() if hasattr(os, "uname") else None
    lines = [
        "运行环境：",
        "  系统：" + read_os_release() + ("（" + " ".join(uname) + "）" if uname else ""),
        "  Python：" + sys.version.split()[0],
        "  Docker 接口：" + endpoint,
        "  Docker 服务端：" + str(info.get("ServerVersion")) + "（" + str(info.get("OperatingSystem")) + "）",
        "  架构：" + str(info.get("Architecture")),
        "  CPU 核数：" + str(info.get("NCPU")),
        "  内存：" + str(round((info.get("MemTotal") or 0) / 1024**3, 1)) + " GiB",
        "  Docker 数据目录：" + str(info.get("DockerRootDir")),
    ]
    try:
        usage = shutil.disk_usage(info.get("DockerRootDir") or "/")
        lines.append("  Docker 目录可用空间：" + str(round(usage.free / 1024**3, 1)) + " GiB")
    except OSError:
        pass
    return "\n".join(lines)


def failure_hints(message):
    lowered = (message or "").lower()
    rules = [
        ("permission denied", "权限不足：确认用 sudo bash 执行；照片目录需要该 uid 可读，数据目录需要该 uid 可写。"),
        ("no space left", "磁盘空间不足：清理 Docker 数据目录或换一个空间充足的安装目录。"),
        ("port is already allocated", "端口被占用：换一个端口重试，例如 bash -s -- --port 18787。"),
        ("address already in use", "端口被占用：换一个端口重试，例如 bash -s -- --port 18787。"),
        ("cannot connect to the docker daemon", "连不上 Docker：确认 Docker 服务已启动，并用 sudo 执行。"),
        ("connection refused", "连不上本机 Docker：确认 Docker 服务正在运行。"),
        ("no matching manifest", "架构不受支持：只支持 amd64（Intel/AMD）和 arm64。"),
        ("tls handshake timeout", "镜像仓库连接超时：可改用 --source oss 从 OSS 下载离线镜像包。"),
        ("i/o timeout", "网络连接超时：检查 NAS 的外网访问，或改用 --source oss。"),
        ("unauthorized", "镜像仓库要求认证：官方镜像是公开的，说明网络被拦截，可改用 --source oss。"),
        ("manifest unknown", "指定版本不存在：检查 --version 的版本号。"),
        ("健康检查", "服务没能启动，日志里有容器最近的输出；常见原因是运行用户无权写 /data。"),
        ("不是空目录", "安装目录里已有内容：换一个空目录，或先确认它是否属于已有安装。"),
        ("安装锁", "另一个安装进程正在运行：等它结束后重试。"),
    ]
    return [hint for keyword, hint in rules if keyword in lowered]


def report_failure(exc):
    message = redact(str(exc).strip()) or exc.__class__.__name__
    LOG.error("安装失败：" + message)
    lines = ["", "──────── 排查信息（可整段复制给我们） ────────",
             "安装器版本：" + INSTALLER_VERSION,
             "失败步骤：" + LOG.stage,
             "失败时间：" + datetime.datetime.now().astimezone().strftime("%Y-%m-%d %H:%M:%S %Z")]
    if LOG.path:
        lines.append("日志文件：" + str(LOG.path))
        lines.append("复制日志：tail -n 200 " + str(LOG.path))
        lines.append("  提示：日志由 root 创建时，命令前要加 sudo 才能读取。")
    else:
        lines.append("日志文件：未能写入，请把上面的完整屏幕输出发给我们。")
    hints = failure_hints(message)
    if hints:
        lines.append("可能原因：")
        lines.extend("  - " + hint for hint in hints)
    lines.append("反馈：把日志发到 " + SUPPORT_EMAIL + "，并说明 NAS 型号和做到哪一步。")
    lines.extend(["──────────────────────────────────────────", ""])
    print("\n".join(lines), file=sys.stderr, flush=True)


def save_log_copy(directory):
    """把日志另存到安装记录目录，方便和备份、配置放在一起。

    失败时也保存：记录目录里已经有 compose.json、data.tar，用户查看备份时能顺手
    拿到同一份日志。只在记录目录已存在时写，不会为此新建目录。
    """
    if not directory or not LOG.path:
        return None
    directory = Path(directory)
    if not directory.is_dir():
        return None
    target = directory / "install.log"
    try:
        shutil.copyfile(LOG.path, target)
        os.chmod(target, 0o600)
    except OSError:
        return None
    print("  本次日志副本：" + str(target), flush=True)
    return target


def main():
    parser = argparse.ArgumentParser(description="Itemory 私有云在线安装与升级（Bash + Python 3.9+ + Docker Compose V2）")
    parser.add_argument("--version", default="1", help="默认使用稳定通道 1；也可指定完整版本号")
    parser.add_argument("--source", choices=("tcr", "ghcr", "oss"), default="tcr", help="默认使用腾讯云国内镜像；ghcr 为海外镜像，oss 为离线包下载")
    parser.add_argument("--download-base", default=DEFAULT_DOWNLOAD_BASE, help="OSS 发布目录的 HTTPS 地址")
    parser.add_argument("--download-dir", default=str(Path.home() / ".cache/itemory-agent/downloads"), help="镜像包缓存的绝对路径，不放入照片或服务数据目录")
    parser.add_argument("--photo-dir", action="append", default=[])
    parser.add_argument("--install-dir")
    parser.add_argument("--user")
    parser.add_argument("--port", type=int)
    parser.add_argument("--timezone")
    parser.add_argument("--container")
    parser.add_argument("--yes", action="store_true")
    parser.add_argument("--migrate-existing", action="store_true",
                        help="非交互确认：把检测到的旧式 Itemory 容器迁移为 Compose 管理")
    parser.add_argument("--log-file", help="安装日志的绝对路径；默认写到 ~/.cache/itemory-agent/logs/")
    parser.add_argument("--debug", action="store_true", help="在屏幕上显示更详细的步骤和命令输出")
    args = parser.parse_args()

    for stream in (sys.stdout, sys.stderr):
        with contextlib.suppress(AttributeError, ValueError):
            stream.reconfigure(encoding="utf-8", errors="replace")
    if args.log_file and not Path(args.log_file).expanduser().is_absolute():
        raise InstallError("--log-file 必须是绝对路径，例如 /tmp/itemory-install.log。")
    LOG.debug_enabled = args.debug
    LOG.open(args.log_file)
    LOG.banner()

    LOG.stage_start(1, "检查运行环境")
    if sys.version_info < (3, 9):
        raise InstallError("需要 Python 3.9 或更新版本。")
    os.umask(0o077)
    LOG.done("Python " + sys.version.split()[0] + "，权限掩码已设为 077")

    LOG.stage_start(2, "检查 Docker 与 Compose")
    endpoint = os.environ.get("DOCKER_HOST") or docker("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
    if not endpoint.startswith("unix://"):
        raise InstallError("请在 NAS 本机执行，并连接本地 Unix Socket Docker；不支持远程 Docker context。")
    info = json.loads(docker("info", "--format", "{{json .}}", log_output=False))
    if info["OSType"] != "linux" or info["Architecture"] not in ("x86_64", "amd64", "aarch64", "arm64"):
        raise InstallError("需要 Linux amd64 或 arm64 Docker。")
    LOG.info("Docker 架构：" + info["Architecture"] + "；系统：" + info.get("OperatingSystem", "Linux")
             + "；CPU " + str(info.get("NCPU")) + " 核")
    LOG.write("ENV", environment_summary(info, endpoint))
    compose_version = docker("compose", "version")
    LOG.done("Docker 服务 " + str(info.get("ServerVersion")) + " 可用")
    LOG.info("Compose：" + compose_version.splitlines()[0])
    args.architecture = info["Architecture"]
    # 使用本地 Docker Socket 对应的唯一锁，避免重复执行产生两个备份或并发替换。
    lock_path = Path(endpoint[len("unix://"):]).resolve().parent / ".itemory-install.lock"
    try:
        lock = lock_path.open("a")
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as exc:
        raise InstallError("无法取得安装锁（已有安装进程或目录不可写）。请使用有 Docker 和目录权限的账号；必要时通过 sudo bash 执行。") from exc
    with lock:
        LOG.debug("已取得安装锁：" + str(lock_path))
        execute(args)


def execute(args):
    global RECORD_DIRECTORY
    started = time.monotonic()
    LOG.stage_start(3, "检测已有安装")
    existing = find_existing(args.container)
    official = bool(existing and official_container(existing) and compose_managed(existing))
    legacy = bool(existing and not official)
    rollback_name = None
    if existing:
        LOG.section("检测结果")
        if args.photo_dir or args.user or args.port or args.timezone or args.install_dir:
            raise InstallError("升级保留原配置，不接受新的照片目录、用户、端口、时区或安装目录参数。")
        if legacy:
            model, project, service, directory = legacy_configuration(existing)
            choose_legacy_migration(args, existing, model)
        else:
            model, project, service, directory = old_configuration(existing)
            LOG.info("类型：已有 Compose 安装")
            LOG.info("容器：" + existing["Name"].lstrip("/") + "（" + existing["Id"][:12] + "）")
        state = existing.get("State") or {}
        if not legacy:
            LOG.info("状态：" + str(state.get("Status")) + "；创建于 " + str(existing.get("Created", "?")))
        LOG.write("INFO", describe_container(existing))
    else:
        model, project, service, directory = None, None, None, None
        LOG.section("检测结果")
        LOG.info("未检测到已有安装，将进行全新安装。")

    LOG.stage_start(4, "获取镜像")
    if args.source == "oss":
        image = resolve_archive_image(args, args.architecture)
    elif args.source == "tcr":
        image = resolve_image(args.version, TCR_REPOSITORY)
    else:
        image = resolve_image(args.version)

    if existing:
        current_id = json.loads(docker("image", "inspect", image, log_output=False))[0]["Id"]
        if not legacy and existing["Image"] == current_id:
            LOG.stage_start(7, "启动并核对服务")
            if not (existing.get("State") or {}).get("Running"):
                confirm(args, "已经是目标镜像，将启动原容器并检查健康状态。")
                docker("start", existing["Id"], stream_output=True)
            health(existing["Id"])
            LOG.done("已经是目标镜像，健康检查通过，配置和配对未变更。")
            LOG.info("总用时：" + str(int(time.monotonic() - started)) + " 秒")
            return
        if legacy:
            confirm(args, "将迁移旧安装：停服、完整备份 /data、改名保留旧容器，再用官方镜像启动 Compose 服务。")
        else:
            confirm(args, "将停服备份整个 /data，并保留原挂载、权限、配置和配对；已有可写照片挂载也会保留。")
    else:
        LOG.stage_start(5, "准备服务配置")
        model, project, service, directory = fresh_configuration(args, image)
        LOG.write("INFO", describe_service(model["services"][service]))
        LOG.done("配置已生成：项目 " + project + "，目录 " + directory)

    root = Path(directory) / ".itemory-online"
    if existing:
        check_record_directory(root.resolve(), existing)
    root.mkdir(parents=True, mode=0o700, exist_ok=True)
    release = root / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
    release.mkdir(mode=0o700)
    RECORD_DIRECTORY = release
    LOG.info("安装记录目录：" + str(release))
    candidate = json.loads(json.dumps(model))
    candidate["services"][service]["image"] = image
    candidate["services"][service]["pull_policy"] = "never"
    config = release / "compose.json"
    write_json(config, candidate if official else compose_escape(candidate))
    command = compose_command([config], project, directory)
    run(command + ["config", "--quiet"])
    LOG.done("Compose 配置校验通过")
    before = {}
    stopped = False
    replacement_started = False
    try:
        if existing:
            LOG.stage_start(6, "升级前备份数据")
            rollback = json.loads(json.dumps(model))
            rollback["services"][service]["image"] = existing["Image"]
            rollback["services"][service]["pull_policy"] = "never"
            write_json(release / "before-compose.json", rollback if official else compose_escape(rollback))
            write_json(release / "before-container.json", existing)
            LOG.info("正在停止原容器（最长等待 30 秒）…")
            docker("stop", "--time", "30", existing["Id"], stream_output=True)
            stopped = True
            LOG.done("原容器已停止")
            archive = release / "data.tar"
            LOG.info("正在备份 /data（照片文件夹不在备份范围内）…")
            with archive.open("xb") as output:
                result = subprocess.run(["docker", "run", "--rm", "--network", "none", "--read-only", "--user", "0:0",
                                         "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true",
                                         "--volumes-from", existing["Id"] + ":ro", "--entrypoint", "/bin/tar",
                                         existing["Image"], "-C", "/data", "-cf", "-", "."], stdout=output)
            if result.returncode:
                LOG.error("备份命令退出码：" + str(result.returncode))
                raise InstallError("备份失败，未替换服务。")
            before = identity_from_archive(archive)
            LOG.done("数据备份完成：" + str(archive) + "（" + str(round(archive.stat().st_size / 1024**2, 1)) + " MiB）")
            if legacy:
                rollback_name = (existing["Name"].lstrip("/") + "-before-migration-"
                                 + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d%H%M%S"))
                LOG.info("正在保留旧容器为 " + rollback_name + "（已停止，不删除）…")
                docker("rename", existing["Id"], rollback_name)
                LOG.done("旧容器已改名保留")
        else:
            LOG.stage_start(6, "确认数据目录权限")
            LOG.done("新安装的数据目录与照片目录权限已在准备配置时预检通过")
        LOG.stage_start(7, "启动并核对服务")
        LOG.info("正在启动容器（不会联网拉取镜像）…")
        replacement_started = True
        run(command + ["up", "-d", "--no-deps", "--no-build", "--pull", "never", service], stream_output=True)
        container_id = run(command + ["ps", "-aq", service]).splitlines()
        if len(container_id) != 1:
            raise InstallError("无法确定唯一的目标容器。")
        LOG.debug("目标容器：" + container_id[0])
        health(container_id[0])
        verify_identity(container_id[0], before)
        final = inspect(container_id[0])
        if existing:
            previous_mount = data_mount(existing)
            current_mount = data_mount(final)
            if (previous_mount["Type"], previous_mount["Source"]) != (current_mount["Type"], current_mount["Source"]):
                raise InstallError("升级后 /data 挂载发生变化。")
            LOG.done("/data 挂载未变化")
        ports = final["NetworkSettings"].get("Ports") or {}
        LOG.section("结果")
        LOG.done("服务已启动并通过健康检查")
        if existing:
            LOG.done("管理员、serverId、配对设备和已有设置均保持不变")
        LOG.info("镜像：" + image)
        for url in management_urls(ports):
            LOG.info("管理网页：" + url)
        if not existing:
            LOG.info("请打开管理网页创建管理员账号，然后在 Itemory 中扫码配对并添加媒体库。")
        else:
            LOG.info("无需重新配对；备份包含凭据，请妥善保管。")
        LOG.info("总用时：" + str(int(time.monotonic() - started)) + " 秒")
        LOG.section("回滚与后续")
        LOG.info("Compose 配置：" + str(config))
        if existing:
            LOG.info("数据备份：" + str(release / "data.tar"))
        if legacy:
            LOG.info("旧容器：" + str(rollback_name) + "（已停止，确认新版正常前不要删除）")
        LOG.info("以后升级：再次执行同一条在线安装命令。")
    except BaseException:
        if stopped and not replacement_started and (existing.get("State") or {}).get("Running"):
            docker("start", existing["Id"])
            LOG.warn("备份或预检失败，已重新启动原容器。")
        elif replacement_started:
            # 新版本可能已经迁移数据库，不能仅换回镜像后宣称已回滚。
            with contextlib.suppress(InstallError):
                run(command + ["stop", service], stream_output=True)
            LOG.warn("安装/升级未通过，已尝试停止目标服务，未删除数据。记录与备份：" + str(release))
            if existing:
                if rollback_name:
                    LOG.warn("旧容器已停止并改名为 " + rollback_name + "；恢复前先核对 data.tar 与新版数据。")
                LOG.warn("恢复时请先把 data.tar 还原到独立目录或新卷，再用 before-compose.json 和旧镜像恢复；"
                         "不要让旧镜像直接读取可能迁移后的数据。")
        raise


try:
    main()
except UserCancelled as exc:
    print(str(exc), flush=True)
    if LOG.path:
        print("日志：" + str(LOG.path) + "（本次没有修改服务，不需要反馈给我们）", flush=True)
    sys.exit(1)
except InstallError as exc:
    report_failure(exc)
    sys.exit(1)
except KeyboardInterrupt:
    LOG.error("安装已中断（Ctrl+C）。服务状态可能未恢复，请检查上方的服务与备份状态。")
    if LOG.path:
        print("日志：" + str(LOG.path), file=sys.stderr, flush=True)
    sys.exit(130)
except (OSError, ValueError, KeyError, tarfile.TarError) as exc:
    LOG.write("ERROR", "异常：\n" + traceback.format_exc())
    report_failure(InstallError("意外错误：" + exc.__class__.__name__ + "：" + str(exc) + "（完整堆栈已写入日志）"))
    sys.exit(1)
except Exception as exc:
    LOG.write("ERROR", "异常：\n" + traceback.format_exc())
    report_failure(InstallError("意外错误：" + exc.__class__.__name__ + "：" + str(exc) + "（完整堆栈已写入日志）"))
    sys.exit(1)
finally:
    # 所有退出路径都在这里收尾：写结束时间、把日志副本留到安装记录目录、关闭文件。
    # 失败时尤其需要这份副本，所以放在 finally 而不是成功分支里。
    LOG.write("INFO", "结束时间：" + datetime.datetime.now().astimezone().isoformat())
    save_log_copy(RECORD_DIRECTORY)
    LOG.close()
PY
}

itemory_online_install "$@"
