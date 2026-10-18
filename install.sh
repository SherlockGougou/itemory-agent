#!/usr/bin/env bash
# 整个函数读取完成后再运行，避免下载中断时执行半份安装程序。
itemory_online_install() {
    command -v python3 >/dev/null 2>&1 || {
        echo '需要 Python 3.9+。请先安装 Python 3，或使用文档中的 Docker Compose 安装方式。' >&2
        return 1
    }
    python3 - "$@" <<'PY'
import argparse
import contextlib
import datetime
import fcntl
import hashlib
import ipaddress
import io
import json
import os
from pathlib import Path
import re
import shlex
import socket
import subprocess
import sys
import tarfile
import time
import urllib.parse
import uuid

REPOSITORY = "ghcr.io/sherlockgougou/itemory-agent"
TCR_REPOSITORY = "ccr.ccs.tencentyun.com/itemory/itemory-agent"
DEFAULT_DOWNLOAD_BASE = "https://itemory-downloads.oss-cn-chengdu.aliyuncs.com/releases/itemory-agent"


class InstallError(Exception):
    pass


def run(args, capture=True, **kwargs):
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.PIPE if capture else None, **kwargs)
    if result.returncode:
        # 不打印可能含凭据的 Compose 内容或完整命令。
        raise InstallError("命令失败：" + " ".join(args[:3]) + "\n" + (result.stderr or "请查看上方错误。"))
    return result.stdout.strip() if capture else ""


def docker(*args, **kwargs):
    return run(["docker", *args], **kwargs)


def inspect(container):
    return json.loads(docker("inspect", container))[0]


def ask(label, default=""):
    try:
        with open("/dev/tty", "w") as terminal:
            terminal.write(label + (" [" + default + "]" if default else "") + ": ")
            terminal.flush()
        with open("/dev/tty", "r") as terminal:
            line = terminal.readline()
    except OSError as exc:
        raise InstallError("没有交互终端，请通过参数提供配置并使用 --yes。") from exc
    if not line:
        raise InstallError("交互输入已关闭。")
    return line.strip() or default


def confirm(args, message):
    print(message, flush=True)
    if not args.yes and ask("继续？输入 yes", "no").lower() != "yes":
        raise InstallError("已取消，服务未变更。")


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
    docker("pull", reference, capture=False)
    image = json.loads(docker("image", "inspect", reference))[0]
    digests = [item for item in image.get("RepoDigests", []) if item.startswith(repository + "@sha256:")]
    if not digests:
        raise InstallError("镜像没有可验证的仓库 digest，安装已停止。")
    return digests[0]


def download(url, destination, max_bytes=None):
    # curl 负责 TLS、超时与进度；签名链接可能含凭据，不回显完整 URL。
    result = subprocess.run(["curl", "--fail", "--location", "--proto", "=https", "--proto-redir", "=https",
                             "--connect-timeout", "20", "--max-time", "1800", "--retry", "2",
                             "--speed-limit", "1024", "--speed-time", "60", "--output", str(destination),
                             *(["--max-filesize", str(max_bytes)] if max_bytes else []), url],
                            stderr=subprocess.PIPE, text=True)
    if result.returncode:
        raise InstallError("OSS 下载失败（curl 错误编号 " + str(result.returncode) + "）。请检查网络、下载地址和访问权限；服务未替换。")
    if max_bytes and destination.stat().st_size > max_bytes:
        raise InstallError("发布清单超过大小限制。")


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
        print("正在获取 OSS 发布清单…", flush=True)
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
            print("正在下载 " + filename + "（" + str(round(size / 1024**2)) + " MiB）…", flush=True)
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
    print("镜像包校验通过，正在导入 Docker…", flush=True)
    docker("image", "load", "--input", str(archive), capture=False)
    image = json.loads(docker("image", "inspect", image_id))[0]
    if image.get("Id") != image_id or image.get("Os") != "linux" or image.get("Architecture") != arch:
        raise InstallError("导入镜像的 ID 或平台与发布清单不一致。")
    if (image.get("Config", {}).get("Labels") or {}).get("org.opencontainers.image.source") != "https://github.com/SherlockGougou/itemory-agent":
        raise InstallError("导入镜像缺少官方来源标记。")
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
        raise InstallError("检测到已有容器，但找不到完整 Compose 源配置。为保留挂载、权限和配对，已停止；请恢复原配置后重试。")
    command = compose_command(files, project, directory)
    model = json.loads(run(command + ["config", "--format", "json"]))
    expected = run(command + ["config", "--hash", service]).split()
    if len(expected) != 2 or expected[1] != labels.get("com.docker.compose.config-hash"):
        raise InstallError("Compose 配置或 Compose 版本与创建现有容器时不一致。请用原配置和原 Compose 版本核对并同步运行状态后重试；未停止服务。")
    data_mount(container)
    return model, project, service, directory


def health(container, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        state = inspect(container)["State"]
        if state.get("Status") in ("exited", "dead"):
            raise InstallError("服务启动后退出，请检查 docker logs。")
        result = subprocess.run(["docker", "exec", container, "itemory-agent", "healthcheck"],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if state.get("Running") and result.returncode == 0:
            return
        time.sleep(2)
    raise InstallError("服务在 120 秒内未通过健康检查。")


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
    return result


def verify_identity(container, before):
    for name, previous in before.items():
        current = json.loads(docker("exec", container, "cat", "/data/" + name))
        if name == "tokens.json":
            if current.get("serverId") != previous.get("serverId"):
                raise InstallError("升级后 serverId 发生变化。")
            tokens = {item["id"]: item["hash"] for item in current.get("tokens", [])}
            if any(tokens.get(item["id"]) != item["hash"] for item in previous.get("tokens", [])):
                raise InstallError("升级后配对设备发生丢失或令牌变化。")
        elif name == "admin.json" and current != previous:
            raise InstallError("升级后管理员凭据发生变化。")
        elif name == "settings.json":
            # 服务可能为新版本补齐默认字段，但已有字段不能被安装器覆盖。
            if any(current.get(key) != value for key, value in previous.items()):
                raise InstallError("升级后已有设置发生变化，请检查备份与版本迁移。")


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


def fresh_configuration(args, image):
    photos = args.photo_dir
    if not photos:
        if args.yes:
            raise InstallError("全新安装使用 --yes 时必须提供 --photo-dir。")
        photos = [ask("照片目录的完整路径")]
        while ask("继续添加照片目录？yes/no", "no").lower() == "yes":
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
               "environment": {"ITEMORY_PRESET": "balanced", "TZ": timezone}, "cpus": 1.5,
               "mem_limit": "2g", "stop_grace_period": "30s"}
    print("照片目录（只读）：" + ", ".join(map(str, photos)))
    print("数据目录：" + str(root / "data") + "；运行用户：" + user + "；端口：" + str(port))
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
    project = "itemory-" + uuid.uuid5(uuid.NAMESPACE_URL, str(root)).hex[:12]
    return {"services": {"itemory-agent": service}}, project, "itemory-agent", str(root)


def find_existing(name):
    ids = docker("ps", "-aq").splitlines()
    if not ids:
        return None
    containers = json.loads(docker("inspect", *ids))
    if name:
        matches = [item for item in containers if item["Name"].lstrip("/") == name]
    else:
        matches = [item for item in containers if official_container(item)
                   or item["Name"] == "/itemory-agent"]
    if len(matches) > 1:
        raise InstallError("检测到多个安装，请用 --container 指定目标容器名。")
    if matches and not official_container(matches[0]):
        raise InstallError("目标容器不是官方 Itemory 镜像，未进行变更。")
    return matches[0] if matches else None


def official_container(container):
    reference = container["Config"]["Image"]
    if reference.split("@")[0].split(":")[0] in (REPOSITORY, TCR_REPOSITORY):
        return True
    labels = container["Config"].get("Labels") or {}
    return (reference == container.get("Image") and re.fullmatch(r"sha256:[a-f0-9]{64}", reference) is not None
            and labels.get("org.opencontainers.image.source") == "https://github.com/SherlockGougou/itemory-agent")


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
    args = parser.parse_args()
    if sys.version_info < (3, 9):
        raise InstallError("需要 Python 3.9 或更新版本。")
    os.umask(0o077)
    endpoint = os.environ.get("DOCKER_HOST") or docker("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
    if not endpoint.startswith("unix://"):
        raise InstallError("请在 NAS 本机执行，并连接本地 Unix Socket Docker；不支持远程 Docker context。")
    info = json.loads(docker("info", "--format", "{{json .}}"))
    if info["OSType"] != "linux" or info["Architecture"] not in ("x86_64", "amd64", "aarch64", "arm64"):
        raise InstallError("需要 Linux amd64 或 arm64 Docker。")
    docker("compose", "version")
    print("Docker 架构：" + info["Architecture"] + "；系统：" + info.get("OperatingSystem", "Linux"), flush=True)
    # 使用本地 Docker Socket 对应的唯一锁，避免重复执行产生两个备份或并发替换。
    lock_path = Path(endpoint[len("unix://"):]).resolve().parent / ".itemory-install.lock"
    try:
        lock = lock_path.open("a")
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as exc:
        raise InstallError("无法取得安装锁（已有安装进程或目录不可写）。请使用有 Docker 和目录权限的账号；必要时通过 sudo bash 执行。") from exc
    with lock:
        args.architecture = info["Architecture"]
        execute(args)


def execute(args):
    existing = find_existing(args.container)
    if existing:
        if args.photo_dir or args.user or args.port or args.timezone or args.install_dir:
            raise InstallError("升级保留原配置，不接受新的照片目录、用户、端口、时区或安装目录参数。")
        model, project, service, directory = old_configuration(existing)
        print("检测到已有 Compose 服务：" + existing["Name"].lstrip("/"))
    if args.source == "oss":
        image = resolve_archive_image(args, args.architecture)
    elif args.source == "tcr":
        image = resolve_image(args.version, TCR_REPOSITORY)
    else:
        image = resolve_image(args.version)
    if existing:
        if existing["Image"] == json.loads(docker("image", "inspect", image))[0]["Id"]:
            if not existing["State"].get("Running"):
                confirm(args, "已经是目标镜像，将启动原容器并检查健康状态。")
                docker("start", existing["Id"])
            health(existing["Id"])
            print("已经是目标镜像，健康检查通过，配置和配对未变更。")
            return
        confirm(args, "将停服备份整个 /data，并保留原挂载、权限、配置和配对；已有可写照片挂载也会保留。")
    else:
        model, project, service, directory = fresh_configuration(args, image)
    root = Path(directory) / ".itemory-online"
    if existing:
        check_record_directory(root.resolve(), existing)
    root.mkdir(mode=0o700, exist_ok=True)
    release = root / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
    release.mkdir(mode=0o700)
    candidate = json.loads(json.dumps(model))
    candidate["services"][service]["image"] = image
    candidate["services"][service]["pull_policy"] = "never"
    config = release / "compose.json"
    write_json(config, candidate if existing else compose_escape(candidate))
    command = compose_command([config], project, directory)
    run(command + ["config", "--quiet"])
    before = {}
    stopped = False
    replacement_started = False
    try:
        if existing:
            rollback = json.loads(json.dumps(model))
            rollback["services"][service]["image"] = existing["Image"]
            rollback["services"][service]["pull_policy"] = "never"
            write_json(release / "before-compose.json", rollback)
            write_json(release / "before-container.json", existing)
            docker("stop", "--time", "30", existing["Id"])
            stopped = True
            archive = release / "data.tar"
            with archive.open("xb") as output:
                result = subprocess.run(["docker", "run", "--rm", "--network", "none", "--read-only", "--user", "0:0",
                                         "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true",
                                         "--volumes-from", existing["Id"] + ":ro", "--entrypoint", "/bin/tar",
                                         existing["Image"], "-C", "/data", "-cf", "-", "."], stdout=output)
            if result.returncode:
                raise InstallError("备份失败，未替换服务。")
            before = identity_from_archive(archive)
            print("数据备份完成：" + str(archive), flush=True)
        replacement_started = True
        run(command + ["up", "-d", "--no-deps", "--no-build", "--pull", "never", service], capture=False)
        container_id = run(command + ["ps", "-aq", service]).splitlines()
        if len(container_id) != 1:
            raise InstallError("无法确定唯一的目标容器。")
        health(container_id[0])
        verify_identity(container_id[0], before)
        final = inspect(container_id[0])
        if existing:
            previous_mount = data_mount(existing)
            current_mount = data_mount(final)
            if (previous_mount["Type"], previous_mount["Source"]) != (current_mount["Type"], current_mount["Source"]):
                raise InstallError("升级后 /data 挂载发生变化。")
        ports = final["NetworkSettings"].get("Ports") or {}
        print("安装/升级完成，健康检查通过；已有配置、管理员和设备配对已核对。")
        print("当前配置：" + str(config))
        print("镜像：" + image)
        for binding in ports.get("8787/tcp") or []:
            host = binding["HostIp"]
            if host in ("0.0.0.0", "::", ""):
                host = "<NAS局域网IP>"
            elif ipaddress.ip_address(host).version == 6:
                host = "[" + host + "]"
            print("管理网页：http://" + host + ":" + binding["HostPort"])
        if not existing:
            print("请打开管理网页创建管理员账号，然后在 Itemory 中扫码配对并添加媒体库。")
        else:
            print("原数据挂载未变化，无需重新配对。备份包含凭据，请妥善保管。")
        print("以后升级：再次执行同一条在线安装命令。")
    except BaseException:
        if stopped and not replacement_started and existing["State"].get("Running"):
            docker("start", existing["Id"])
            print("备份或预检失败，已重新启动原容器。", file=sys.stderr)
        elif replacement_started:
            # 新版本可能已经迁移数据库，不能仅换回镜像后宣称已回滚。
            with contextlib.suppress(InstallError):
                run(command + ["stop", service])
            print("安装/升级未通过，已尝试停止目标服务，未删除数据。记录与备份：" + str(release), file=sys.stderr)
            if existing:
                print("恢复时请先把 data.tar 还原到独立目录或新卷，再用 before-compose.json 和旧镜像恢复；不要让旧镜像直接读取可能迁移后的数据。", file=sys.stderr)
        raise


try:
    main()
except (InstallError, OSError, ValueError, KeyError, tarfile.TarError) as exc:
    print("安装失败：" + str(exc), file=sys.stderr)
    sys.exit(1)
except KeyboardInterrupt:
    print("安装已中断。请检查上方的服务与备份状态。", file=sys.stderr)
    sys.exit(130)
PY
}
itemory_online_install "$@"
