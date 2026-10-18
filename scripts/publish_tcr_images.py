import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import uuid

SOURCE = "ghcr.io/sherlockgougou/itemory-agent"


def run(*args, capture=True):
    result = subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE if capture else None)
    return result.stdout.strip() if capture else ""


def manifest(reference, anonymous=False):
    options = ["--no-creds"] if anonymous else []
    return json.loads(run("skopeo", "inspect", *options, "--raw", "docker://" + reference))


def platforms(index):
    result = {}
    for entry in index.get("manifests", []):
        platform = entry.get("platform", {})
        if platform.get("os") == "linux" and platform.get("architecture") in ("amd64", "arm64"):
            arch = platform["architecture"]
            if arch in result:
                raise RuntimeError("同一架构有多个镜像，拒绝猜测：" + arch)
            result[arch] = entry["digest"]
    if set(result) != {"amd64", "arm64"}:
        raise RuntimeError("镜像清单必须包含 Linux amd64 和 arm64")
    return result


def verify(reference, expected):
    index = manifest(reference, True)
    images = platforms(index)
    if len(index["manifests"]) != 2:
        raise RuntimeError("国内镜像清单包含预期以外的制品")
    for arch, digest in images.items():
        image = manifest(reference.rsplit(":", 1)[0] + "@" + digest, True)
        source = expected[arch]
        if image["config"]["digest"] != source["config"]["digest"]:
            raise RuntimeError("国内镜像 config 与官方源不一致：" + arch)
        if [layer["digest"] for layer in image["layers"]] != [layer["digest"] for layer in source["layers"]]:
            raise RuntimeError("国内镜像 layer 与官方源不一致：" + arch)
    return index


def smoke(reference, expected, output):
    anonymous = output / ("anonymous-docker-" + uuid.uuid4().hex[:8])
    anonymous.mkdir(parents=True)
    (anonymous / "config.json").write_text("{}\n")
    endpoint = os.environ.get("DOCKER_HOST") or run("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
    command = ["docker", "--host", endpoint, "--config", str(anonymous)]
    for arch, source in expected.items():
        platform = "linux/" + arch
        run(*command, "pull", "--platform", platform, reference, capture=False)
        # config digest 就是 Image ID，直接检查它也兼容 Runner 较旧的 Docker CLI。
        metadata = json.loads(run(*command, "image", "inspect", source["config"]["digest"]))[0]
        if metadata["Os"] != "linux" or metadata["Architecture"] != arch:
            raise RuntimeError("Docker 拉取的平台不正确：" + arch)
        config = json.loads(run("skopeo", "inspect", "--no-creds", "--config", "docker://" + reference,
                                "--override-arch", arch, "--override-os", "linux"))
        if metadata["RootFS"]["Layers"] != config["rootfs"]["diff_ids"]:
            raise RuntimeError("Docker 本地镜像的实际内容不一致")
        actual = run(*command, "run", "--rm", "--network", "none", "--platform", platform, reference, "version")
        if actual.split()[-1].lstrip("v") != reference.rsplit(":", 1)[1]:
            raise RuntimeError("国内镜像二进制版本不正确：" + actual)
        print("匿名 Docker 拉取与版本验证通过：" + platform, flush=True)


def publish(version, repository, output):
    source_ref = SOURCE + ":" + version
    source_index = manifest(source_ref, True)
    children = platforms(source_index)
    expected = {arch: manifest(SOURCE + "@" + digest, True) for arch, digest in children.items()}
    target = repository + ":" + version
    output.mkdir(parents=True, exist_ok=True)
    existing = subprocess.run(["skopeo", "inspect", "--no-creds", "--raw", "docker://" + target],
                              text=True, capture_output=True)
    # 国内仅复制两种可运行镜像；GHCR 保留原始 provenance/SBOM。
    # v2s2 转换只改变 manifest 的格式，随后逐项核对 config 和 layer digest。
    if existing.returncode == 0:
        verify(target, expected)
        print("版本已存在且内容一致，复用已发布镜像。")
    elif "manifest unknown" in existing.stderr.lower() or "manifest_unknown" in existing.stderr.lower():
        for arch, digest in children.items():
            run("skopeo", "copy", "--format", "v2s2", "--retry-times", "3",
                "docker://" + SOURCE + "@" + digest, "docker://" + target + "-" + arch, capture=False)
        run("docker", "manifest", "create", target, target + "-amd64", target + "-arm64", capture=False)
        run("docker", "manifest", "push", "--purge", target, capture=False)
    else:
        raise RuntimeError("无法匿名检查目标仓库，请先确认仓库为公开且网络正常：" + existing.stderr)
    index = verify(target, expected)
    smoke(target, expected, output)
    (output / "tcr-verification.json").write_text(json.dumps({"source": source_ref, "target": target,
        "platforms": children, "manifest": index, "result": "PASS"}, indent=2) + "\n")
    # 只让当前 GHCR 稳定版本推进 :1；补发旧版本不会使国内稳定通道降级。
    if "-" not in version and manifest(SOURCE + ":1", True) == source_index:
        run("skopeo", "copy", "--all", "--format", "v2s2", "--retry-times", "3",
            "docker://" + target, "docker://" + repository + ":1", capture=False)
        verify(repository + ":1", expected)
        print("国内稳定通道 :1 已验证并更新。")
    else:
        print("此版本不是当前 GHCR 稳定版本，未更新 :1。")


def main():
    parser = argparse.ArgumentParser(description="把已发布官方镜像同步到腾讯云个人版，并验证匿名双架构拉取")
    parser.add_argument("--version", required=True)
    parser.add_argument("--repository", default=os.environ.get("TCR_REPOSITORY"))
    parser.add_argument("--output", type=Path, default=Path(".agent-temp/tcr-release"))
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?", args.version):
        parser.error("需要完整版本号，例如 0.5.0")
    if not args.repository or not re.fullmatch(r"ccr\.ccs\.tencentyun\.com/[a-z0-9][a-z0-9_-]*/itemory-agent", args.repository):
        parser.error("需要腾讯云个人版的官方 itemory-agent 仓库地址")
    publish(args.version, args.repository, args.output)


if __name__ == "__main__":
    main()
