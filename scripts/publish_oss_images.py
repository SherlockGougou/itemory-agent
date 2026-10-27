import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import urllib.error
import urllib.request
from package_offline_bundle import package_archive

REPOSITORY = "ghcr.io/sherlockgougou/itemory-agent"


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def package_image(version, arch, output):
    reference = REPOSITORY + ":" + version
    platform = "linux/" + arch
    subprocess.run(["docker", "pull", "--platform", platform, reference], check=True)
    metadata = json.loads(run("docker", "image", "inspect", "--platform", platform, reference))[0]
    if metadata["Os"] != "linux" or metadata["Architecture"] != arch:
        raise RuntimeError("镜像平台与待发布架构不一致")
    if (metadata["Config"].get("Labels") or {}).get("org.opencontainers.image.source") != "https://github.com/SherlockGougou/itemory-agent":
        raise RuntimeError("镜像缺少官方来源标记")
    actual_version = run("docker", "run", "--rm", "--network", "none", "--platform", platform, reference, "version")
    if actual_version.split()[-1].lstrip("v") != version:
        raise RuntimeError("镜像内版本与发布版本不一致：" + actual_version)
    archive = output / ("itemory-agent-" + version + "-" + arch + ".tar.gz")
    # 每个压缩包只导出一种架构，避免 containerd 镜像存储把所有已缓存平台一起导出。
    with archive.open("xb") as target:
        with gzip.GzipFile(fileobj=target, mode="wb", compresslevel=6, mtime=0) as compressed:
            with subprocess.Popen(["docker", "image", "save", "--platform", platform, reference], stdout=subprocess.PIPE) as process:
                shutil.copyfileobj(process.stdout, compressed)
                if process.wait():
                    raise RuntimeError("docker image save 失败")
    checksum = sha256(archive)
    checksums = output / (archive.name + ".sha256")
    checksums.write_text(checksum + "  " + archive.name + "\n")
    offline = package_archive(archive, version, arch, output)
    return {"file": archive.name, "sha256": checksum, "size": archive.stat().st_size,
            "image_id": metadata["Id"],
            "offline_bundle": offline["file"], "offline_sha256": offline["sha256"]}


def publish(version, output, manifest):
    required = ("OSS_BUCKET", "OSS_REGION", "OSS_PREFIX", "OSS_DOWNLOAD_BASE", "OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET")
    if any(not os.environ.get(name) for name in required):
        raise RuntimeError("缺少 OSS 发布配置；请按 docs/oss-distribution.zh-CN.md 配置变量与 Secrets")
    bucket, prefix = os.environ["OSS_BUCKET"], os.environ["OSS_PREFIX"].strip("/")
    base = os.environ["OSS_DOWNLOAD_BASE"].rstrip("/")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]+[a-z0-9]", bucket) or not re.fullmatch(r"[A-Za-z0-9_/-]+", prefix) or ".." in prefix:
        raise RuntimeError("OSS Bucket 或发布前缀不正确")
    if not base.startswith("https://") or "?" in base or "#" in base:
        raise RuntimeError("OSS_DOWNLOAD_BASE 必须是稳定的 HTTPS 下载目录")
    environment = dict(os.environ, OSS_REGION=os.environ["OSS_REGION"])
    def upload(path, key, mutable=False):
        # 默认不改 Bucket/Object ACL；下载权限由仅针对发布前缀的 Bucket Policy 提供。
        digest = sha256(path)
        if not mutable:
            try:
                with urllib.request.urlopen(urllib.request.Request(base + "/" + key, method="HEAD"), timeout=30) as response:
                    if int(response.headers["Content-Length"]) == path.stat().st_size:
                        if response.headers.get("x-oss-meta-sha256") == digest:
                            return
                        # 首次可能由控制台上传，没有 SHA-256 元数据；完整读取确认后才能重用。
                        existing_digest = hashlib.sha256()
                        with urllib.request.urlopen(base + "/" + key, timeout=60) as existing:
                            for chunk in iter(lambda: existing.read(1024 * 1024), b""):
                                existing_digest.update(chunk)
                        if existing_digest.hexdigest() == digest:
                            return
                    raise RuntimeError("不可覆盖已发布的不同内容：" + key)
            except urllib.error.HTTPError as error:
                if error.code != 404:
                    raise RuntimeError("CDN 下载地址无法访问：HTTP " + str(error.code)) from error
        if path.stat().st_size > 5 * 1024**3:
            raise RuntimeError("当前发布器仅支持不超过 5 GiB 的单个文件")
        command = ["ossutil", "api", "put-object", "--bucket", bucket, "--key", prefix + "/" + key,
                   "--body", "file://" + str(path.resolve()), "--region", os.environ["OSS_REGION"],
                   "--metadata", "sha256=" + digest, "--cache-control",
                   "public,max-age=60" if mutable else "public,max-age=31536000,immutable"]
        if not mutable:
            command.append("--forbid-overwrite")
        subprocess.run(command, check=True, env=environment)
    # 先上传不可变文件，清单和主版本入口最后更新；失败时不提前切换安装通道。
    for path in sorted(output.iterdir()):
        if path.is_file():
            upload(path, "versions/" + version + "/" + path.name)
    for entry in manifest["images"].values():
        for name, expected in ((entry["file"], entry["sha256"]),
                               (entry["offline_bundle"], entry["offline_sha256"])):
            request = urllib.request.Request(base + "/versions/" + version + "/" + name)
            digest = hashlib.sha256()
            size = 0
            with urllib.request.urlopen(request, timeout=60) as response:
                for chunk in iter(lambda: response.read(1024 * 1024), b""):
                    digest.update(chunk)
                    size += len(chunk)
            if digest.hexdigest() != expected or size != (output / name).stat().st_size:
                raise RuntimeError("OSS 公网下载验收失败，未更新主版本通道")
    # 单个 ZIP 内含版本一致的镜像、配置和校验文件，避免手动下载多个可变对象时版本混用。
    if "-" not in version:
        channel = version.split(".")[0]
        # 兼容现有 :1 稳定发布通道，包括当前 0.x 正式版本。
        if channel == "0":
            channel = "1"
        for arch, entry in manifest["images"].items():
            upload(output / entry["offline_bundle"], "channels/" + channel + "/itemory-agent-" + arch + "-offline.zip", True)
            alias_checksum = output / ("channel-" + arch + ".sha256")
            alias_checksum.write_text(entry["offline_sha256"] + "  itemory-agent-" + arch + "-offline.zip\n")
            upload(alias_checksum, "channels/" + channel + "/itemory-agent-" + arch + "-offline.zip.sha256", True)
        upload(output / "manifest.json", "channels/" + channel + ".json", True)


def main():
    parser = argparse.ArgumentParser(description="将已发布 GHCR 镜像打包并分发到 OSS；不重新构建镜像")
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--package-only", action="store_true", help="仅生成并校验本地产物，不上传")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?", args.version):
        parser.error("需要完整版本号，例如 0.5.0")
    if not args.package_only and any(not os.environ.get(name) for name in (
            "OSS_BUCKET", "OSS_REGION", "OSS_PREFIX", "OSS_DOWNLOAD_BASE", "OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET")):
        parser.error("缺少 OSS 配置或上传凭据；请先配置 GitHub Actions Variables/Secrets，或使用 --package-only")
    args.output.mkdir(parents=True, exist_ok=False)
    manifest = {"schema": 1, "version": args.version, "repository": REPOSITORY, "images": {}}
    for arch in ("amd64", "arm64"):
        manifest["images"][arch] = package_image(args.version, arch, args.output)
    (args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    if not args.package_only:
        publish(args.version, args.output, manifest)


if __name__ == "__main__":
    main()
