# 使用真实 Docker 镜像和受信任的本地 HTTPS 服务验证 OSS 安装，不访问阿里云账号。
import argparse
import http.server
import json
import os
from pathlib import Path
import ssl
import subprocess
import threading
import unittest
import uuid

from test_online_installer_docker import DockerInstallerTests

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--packages', type=Path, required=True, help='publish_oss_images.py 生成的 0.5.0 产物目录')
    args = parser.parse_args()
    fixture = ROOT / '.agent-temp' / ('oss-https-' + uuid.uuid4().hex[:8])
    version_dir = fixture / 'versions/0.5.0'
    version_dir.mkdir(parents=True)
    for path in args.packages.iterdir():
        if path.suffix != '.zip':
            os.link(path.resolve(), version_dir / path.name)
    manifest = json.loads((version_dir / 'manifest.json').read_text())
    if manifest['version'] != '0.5.0':
        raise RuntimeError('集成测试需要 0.5.0 正式版本')
    cert, key = fixture / 'cert.pem', fixture / 'key.pem'
    config = fixture / 'openssl.cnf'
    config.write_text('[req]\ndistinguished_name=dn\nx509_extensions=ext\nprompt=no\n[dn]\nCN=localhost\n'
                      '[ext]\nsubjectAltName=DNS:localhost,IP:127.0.0.1\nbasicConstraints=critical,CA:TRUE\n'
                      'keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\nextendedKeyUsage=serverAuth\n')
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-keyout', str(key), '-out', str(cert), '-config', str(config)], check=True, capture_output=True)
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0),
        lambda *args, **kwargs: http.server.SimpleHTTPRequestHandler(*args, directory=str(fixture), **kwargs))
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    os.environ['CURL_CA_BUNDLE'] = str(cert)
    os.environ['ITEMORY_INSTALL_TEST_SOURCE'] = 'oss'
    os.environ['ITEMORY_INSTALL_TEST_DOWNLOAD_BASE'] = 'https://localhost:' + str(server.server_port)
    try:
        result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(DockerInstallerTests))
        if not result.wasSuccessful():
            raise SystemExit(1)
    finally:
        server.shutdown()
        server.server_close()


if __name__ == '__main__':
    main()
