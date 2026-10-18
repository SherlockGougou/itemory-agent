# 显式运行的 Docker 集成测试；只创建和清理本测试命名的容器、卷与网络。
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import unittest
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]


def command(*args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, **kwargs)


class DockerInstallerTests(unittest.TestCase):
    def test_online_install_and_legacy_upgrade(self):
        fixture = ROOT / '.agent-temp' / ('installer-docker-' + uuid.uuid4().hex[:8])
        fixture.mkdir(parents=True)
        photo = fixture / 'photos $ with spaces'
        photo.mkdir()
        (photo / 'sample.txt').write_text('original photo fixture')
        suffix = uuid.uuid4().hex[:8]
        fresh_name = 'itemory-install-test-' + suffix
        legacy_name = 'itemory-upgrade-test-' + suffix
        volume = 'itemory-upgrade-data-' + suffix
        project = 'itemory-upgrade-' + suffix
        legacy_file = fixture / 'legacy.json'
        logs = []
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0),
                    lambda *args, **kwargs: http.server.SimpleHTTPRequestHandler(*args, directory=str(ROOT), **kwargs))
        threading.Thread(target=server.serve_forever, daemon=True).start()
        url = 'http://127.0.0.1:' + str(server.server_port) + '/install.sh'
        def install(name, *args, success=True):
            script = 'set -o pipefail; curl -fsSL "$1" | bash -s -- --yes --version 0.5.0 --container "$2" "${@:3}"'
            source = os.environ.get('ITEMORY_INSTALL_TEST_SOURCE', 'ghcr')
            extra = ['--source', source]
            if source == 'oss':
                extra += ['--download-base', os.environ['ITEMORY_INSTALL_TEST_DOWNLOAD_BASE'],
                          '--download-dir', str(fixture / 'downloads')]
            result = command('bash', '-c', script, 'installer-test', url, name, *extra, *args)
            logs.append(result.stdout + result.stderr)
            self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
            return result
        def docker(*args):
            result = command('docker', *args)
            self.assertEqual(result.returncode, 0, result.stderr)
            return result.stdout
        def wait_api(port):
            for _ in range(60):
                try:
                    with urllib.request.urlopen('http://127.0.0.1:' + str(port) + '/api/v1/health', timeout=2) as response:
                        return json.load(response)
                except OSError:
                    time.sleep(1)
            self.fail('legacy service did not start')
        base = ['docker', 'compose', '-p', project, '-f', str(legacy_file)]
        try:
            install(fresh_name, '--install-dir', str(fixture / 'fresh'), '--photo-dir', str(photo),
                    '--user', str(os.getuid()) + ':' + str(os.getgid()), '--port', '28788')
            fresh = json.loads(docker('inspect', fresh_name))[0]
            self.assertFalse(next(m for m in fresh['Mounts'] if m['Destination'] == '/volumes/photos1')['RW'])
            denied = command('docker', 'exec', fresh_name, 'sh', '-c', 'echo changed > /volumes/photos1/sample.txt')
            self.assertNotEqual(denied.returncode, 0)
            self.assertEqual((photo / 'sample.txt').read_text(), 'original photo fixture')
            result = install(fresh_name)
            self.assertIn('已经是目标镜像', result.stdout)
            self.assertEqual(json.loads(docker('inspect', fresh_name))[0]['Id'], fresh['Id'])
            # 从旧官方版本和命名卷升级，走真实管理员与设备配对 API。
            model = {'services': {'itemory-agent': {'image': 'ghcr.io/sherlockgougou/itemory-agent:0.4.0',
                     'container_name': legacy_name, 'user': '1000:1000',
                     'environment': {'TEST_LITERAL': 'secret$$with$${braces}', 'TZ': 'Asia/Shanghai'},
                     'volumes': [volume + ':/data', {'type': 'bind', 'source': str(photo).replace('$', '$$'),
                                 'target': '/volumes/photos', 'read_only': True}], 'ports': ['127.0.0.1:28789:8787']}},
                     'volumes': {volume: {'name': volume}}}
            legacy_file.write_text(json.dumps(model))
            result = command(*base, 'up', '-d', '--pull', 'never')
            self.assertEqual(result.returncode, 0, result.stderr)
            before_health = wait_api(28789)
            cookies = http.cookiejar.CookieJar()
            opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
            def api(path, body=None, token=None):
                headers = {'Content-Type': 'application/json'}
                if token:
                    headers['Authorization'] = 'Bearer ' + token
                request = urllib.request.Request('http://127.0.0.1:28789/api/v1/' + path,
                            data=json.dumps(body).encode() if body is not None else None, headers=headers)
                with opener.open(request, timeout=10) as response:
                    return json.load(response)
            api('admin/setup', {'username': 'installer-test', 'password': 'installer-test-password-0123456789'})
            claim = api('claim', {})
            paired = api('pair', {'token': claim['code'], 'deviceName': 'Installer test phone'})
            token = paired['token']
            settings = api('settings', token=token)
            settings['scanSchedule'] = '04:12'
            request = urllib.request.Request('http://127.0.0.1:28789/api/v1/settings', method='PUT',
                        data=json.dumps(settings).encode(), headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
            with opener.open(request, timeout=10) as response:
                json.load(response)
            before = json.loads(docker('inspect', legacy_name))[0]
            photo_identity = docker('exec', legacy_name, 'sh', '-ec',
                                    'stat -c "%d:%i:%u:%g:%a" /volumes/photos/sample.txt; sha256sum /volumes/photos/sample.txt')
            before_model = json.loads(docker('compose', '-p', project, '-f', str(legacy_file), 'config', '--format', 'json'))
            install(legacy_name)
            after = json.loads(docker('inspect', legacy_name))[0]
            self.assertNotEqual(before['Id'], after['Id'])
            before_mounts, after_mounts = json.loads(json.dumps([before['Mounts'], after['Mounts']]))
            # Docker Desktop 可把同一宿主路径显示为 /host_mnt/Users 或 /Users；
            # 同时核对原始 Compose 和真实文件身份，避免仅规范化字符串掩盖挂载变化。
            if docker('info', '--format', '{{.OperatingSystem}}').strip() == 'Docker Desktop':
                for mount in before_mounts + after_mounts:
                    if mount['Type'] == 'bind' and mount['Source'].startswith('/host_mnt/'):
                        mount['Source'] = mount['Source'][len('/host_mnt'):]
            self.assertEqual(before_mounts, after_mounts)
            self.assertEqual(docker('exec', legacy_name, 'sh', '-ec',
                                   'stat -c "%d:%i:%u:%g:%a" /volumes/photos/sample.txt; sha256sum /volumes/photos/sample.txt'), photo_identity)
            candidate = json.loads(Path(after['Config']['Labels']['com.docker.compose.project.config_files']).read_text())
            self.assertEqual(candidate['services']['itemory-agent']['volumes'], before_model['services']['itemory-agent']['volumes'])
            self.assertEqual([v for v in before['Config']['Env'] if v.startswith('TEST_LITERAL=')],
                             [v for v in after['Config']['Env'] if v.startswith('TEST_LITERAL=')])
            upgraded = wait_api(28789)
            self.assertEqual(upgraded['version'], '0.5.0')
            self.assertEqual(upgraded['serverId'], before_health['serverId'])
            self.assertEqual(api('settings', token=token)['scanSchedule'], '04:12')
            api('admin/login', {'username': 'installer-test', 'password': 'installer-test-password-0123456789'})
            record = Path(after['Config']['Labels']['com.docker.compose.project.config_files']).parent
            self.assertTrue((record / 'data.tar').is_file())
            # 修改当前配置后，在线升级必须在停服前拒绝。
            current = record / 'compose.json'
            changed = json.loads(current.read_text())
            changed['services']['itemory-agent']['environment']['TZ'] = 'UTC'
            current.write_text(json.dumps(changed))
            install(legacy_name, success=False)
            self.assertTrue(json.loads(docker('inspect', legacy_name))[0]['State']['Running'])
            self.assertEqual(api('settings', token=token)['scanSchedule'], '04:12')
        finally:
            server.shutdown()
            server.server_close()
            for name in (fresh_name, legacy_name):
                found = command('docker', 'inspect', name)
                if found.returncode == 0:
                    container = json.loads(found.stdout)[0]
                    labels = container['Config'].get('Labels', {})
                    files = labels.get('com.docker.compose.project.config_files')
                    if files:
                        command('docker', 'compose', '-p', labels['com.docker.compose.project'], '-f', files,
                                'down', '--remove-orphans')
            command('docker', 'volume', 'rm', volume)
            (fixture / 'installer-output.log').write_text('\n'.join(logs))


if __name__ == '__main__':
    unittest.main()
