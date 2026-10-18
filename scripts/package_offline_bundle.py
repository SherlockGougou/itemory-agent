import argparse
import hashlib
from pathlib import Path
import re
import zipfile

import yaml

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "ghcr.io/sherlockgougou/itemory-agent"


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def package_archive(archive, version, arch, output):
    output.mkdir(parents=True, exist_ok=True)
    bundle = output / ("itemory-agent-" + version + "-" + arch + "-offline.zip")
    checksum = sha256(archive)
    instructions = (
        "Itemory " + version + " — linux/" + arch + "\n\n"
        "中文：这是通用离线包，适用于支持 Docker 的群晖、飞牛、绿联、极空间、威联通、\n"
        "TrueNAS、Unraid、OpenMediaVault 和 Linux。Intel/AMD 选 amd64；64 位 ARM 选 arm64。\n"
        "1. 解压 ZIP，将 .tar.gz 文件导入 Docker 镜像列表，或执行 docker load -i <镜像包>。\n"
        "   若系统只接受 .tar，请先解压 .tar.gz；不要使用 docker import。\n"
        "2. 从 compose/ 选择系统对应的 YAML，核对照片与数据目录、uid:gid 和端口后创建项目。\n"
        "   保留 pull_policy: never；默认照片只读，不需要特权模式。\n"
        "3. 打开 http://<NAS局域网IP>:8787，创建管理员账号，再用 Itemory 扫码配对。\n"
        "已有安装：升级前备份 /data，保留原配置与数据卷；不要用新安装模板覆盖现有服务。\n\n"
        "English: universal offline package for Docker-capable Synology, fnOS, UGREEN, ZSPACE, QNAP,\n"
        "TrueNAS, Unraid, OpenMediaVault and Linux. Choose amd64 for Intel/AMD, arm64 for 64-bit ARM.\n"
        "Extract the ZIP and import the .tar.gz into Docker, or run docker load -i <archive>.\n"
        "Decompress gzip first if the UI only accepts .tar. Do not use docker import.\n"
        "Select the matching YAML from compose/, check paths, uid:gid and ports, then create the project.\n"
        "Keep pull_policy: never. Photo mounts are read-only; privileged mode is unnecessary.\n"
        "Open http://<NAS-IP>:8787 to create an administrator and pair Itemory.\n"
        "For upgrades, back up /data and retain existing configuration and volumes.\n\n"
        "https://itemory.gouqinglin.com/nas-guide.html\n"
    )
    with zipfile.ZipFile(bundle, "x", compression=zipfile.ZIP_STORED) as package:
        package.write(archive, archive.name)
        package.writestr("SHA256SUMS", checksum + "  " + archive.name + "\n")
        package.writestr("README.txt", instructions)
        for template in sorted((ROOT / "deploy/compose").glob("*.yml")):
            model = yaml.safe_load(template.read_text())
            service = model["services"]["itemory-agent"]
            service["image"] = REPOSITORY + ":" + version
            service["pull_policy"] = "never"
            package.writestr("compose/" + template.name, yaml.safe_dump(model, allow_unicode=True, sort_keys=False))
    digest = sha256(bundle)
    bundle.with_suffix(bundle.suffix + ".sha256").write_text(digest + "  " + bundle.name + "\n")
    return {"file": bundle.name, "sha256": digest, "size": bundle.stat().st_size}


def main():
    parser = argparse.ArgumentParser(description="把既有官方镜像归档与各系统配置打包，不重建镜像")
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?", args.version):
        parser.error("需要完整版本号")
    print(package_archive(args.archive, args.version, args.arch, args.output))


if __name__ == "__main__":
    main()
