import ast
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import unittest
import uuid
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
source = (ROOT / 'install.sh').read_text().split("<<'PY'\n", 1)[1].split('\nPY\n', 1)[0]
module = ast.parse(source)
module.body = [node for node in module.body if not isinstance(node, ast.Try)]
namespace = {}
exec(compile(module, str(ROOT / 'install.sh'), 'exec'), namespace)


class InstallerTests(unittest.TestCase):
    def test_domestic_and_legacy_repositories_are_recognized_without_accepting_other_namespaces(self):
        for reference in (namespace['REPOSITORY'] + ':0.5.0', namespace['TCR_REPOSITORY'] + ':1',
                          namespace['TCR_REPOSITORY'] + '@sha256:' + 'a' * 64):
            self.assertTrue(namespace['official_container']({'Config': {'Image': reference}}))
        self.assertFalse(namespace['official_container']({'Config': {'Image': 'ccr.ccs.tencentyun.com/other/itemory-agent:1'}}))

    def test_domestic_resolver_pins_only_the_requested_repository_digest(self):
        expected = namespace['TCR_REPOSITORY'] + '@sha256:' + 'a' * 64
        def docker(*args, **kwargs):
            if args[:2] == ('image', 'inspect'):
                return json.dumps([{'RepoDigests': [namespace['REPOSITORY'] + '@sha256:' + 'b' * 64, expected]}])
            return ''
        with patch.dict(namespace, docker=docker):
            self.assertEqual(namespace['resolve_image']('1', namespace['TCR_REPOSITORY']), expected)
            with self.assertRaisesRegex(namespace['InstallError'], '官方镜像源'):
                namespace['resolve_image']('1', 'ccr.ccs.tencentyun.com/other/itemory-agent')

    def test_photo_path_requires_explicit_absolute_directory(self):
        for path in ('', '.', 'photos'):
            with self.assertRaisesRegex(namespace['InstallError'], '绝对路径'):
                namespace['safe_directory'](path, '照片路径')

    def failed_upgrade(self, backup_code, fail_replacement):
        directory = ROOT / '.agent-temp' / ('installer-failure-' + uuid.uuid4().hex[:8])
        directory.mkdir(parents=True)
        existing = {'Id': 'original-container', 'Name': '/original', 'Image': 'old-image',
                    'State': {'Running': True}}
        args = SimpleNamespace(container=None, version='1', photo_dir=[], user=None, port=None,
                               timezone=None, install_dir=None, yes=True, source='ghcr')
        calls = []
        def fake_docker(*args, **kwargs):
            calls.append(args)
            return '[{"Id":"new-image"}]' if args[:2] == ('image', 'inspect') else ''
        def fake_run(args, **kwargs):
            calls.append(tuple(args))
            if 'up' in args and fail_replacement:
                raise namespace['InstallError']('replacement failed')
            return ''
        replacements = dict(find_existing=lambda name: existing, resolve_image=lambda version: 'new-digest',
                            old_configuration=lambda container: ({'services': {'agent': {}}}, 'project', 'agent', str(directory)),
                            confirm=lambda *args: None, check_record_directory=lambda *args: None,
                            docker=fake_docker, run=fake_run, identity_from_archive=lambda path: {},
                            subprocess=SimpleNamespace(run=lambda *args, **kwargs: SimpleNamespace(returncode=backup_code)))
        with patch.dict(namespace, **replacements):
            with self.assertRaises(namespace['InstallError']):
                namespace['execute'](args)
        return calls, directory

    def test_backup_failure_restarts_original_without_replacement(self):
        calls, _ = self.failed_upgrade(1, False)
        self.assertIn(('start', 'original-container'), calls)
        self.assertFalse(any('up' in call for call in calls))

    def test_replacement_failure_stops_target_and_keeps_backup(self):
        calls, directory = self.failed_upgrade(0, True)
        self.assertFalse(any(call[0] == 'start' for call in calls))
        self.assertTrue(any('stop' in call and call[0] == 'docker' for call in calls))
        self.assertEqual(len(list(directory.glob('.itemory-online/*/data.tar'))), 1)

    def test_identity_accepts_last_seen_but_rejects_lost_pairing(self):
        before = {'tokens.json': {'serverId': 'server', 'tokens': [{'id': 'phone', 'hash': 'secret-hash', 'lastSeen': 'old'}]},
                  'admin.json': {'username': 'admin', 'passwordHash': 'hash'},
                  'settings.json': {'libraries': [{'id': 'library', 'path': '/volumes/photos'}]}}
        current = json.loads(json.dumps(before))
        current['tokens.json']['tokens'][0]['lastSeen'] = 'new'
        with patch.dict(namespace, docker=lambda *args: json.dumps(current[args[-1].split('/')[-1]])):
            namespace['verify_identity']('container', before)
            current['tokens.json']['tokens'] = []
            with self.assertRaisesRegex(namespace['InstallError'], '配对'):
                namespace['verify_identity']('container', before)

    def test_settings_changes_are_not_silently_accepted(self):
        with patch.dict(namespace, docker=lambda *args: '{"libraries": []}'):
            with self.assertRaisesRegex(namespace['InstallError'], '设置'):
                namespace['verify_identity']('container', {'settings.json': {'libraries': [{'id': 'keep'}]}})

    def test_data_layout_cannot_silently_change(self):
        container = {'Config': {'Env': ['ITEMORY_CACHE_DIR=/other']},
                     'Mounts': [{'Destination': '/data', 'Type': 'bind', 'RW': True}]}
        with self.assertRaisesRegex(namespace['InstallError'], '/data'):
            namespace['data_mount'](container)

    def test_compose_roundtrip_preserves_literal_dollar_and_quotes(self):
        model = {'services': {'agent': {'image': 'example:test', 'environment': {'VALUE': 'a$HOME${secret} "中"'},
                                      'volumes': [{'type': 'bind', 'source': '/path with $ and \'quote', 'target': '/media'}]}}}
        escaped = namespace['compose_escape'](model)
        result = subprocess.run(['docker', 'compose', '-p', 'installer-unit', '-f', '-', 'config', '--format', 'json'],
                                input=json.dumps(escaped), text=True, capture_output=True, check=True)
        actual = json.loads(result.stdout)['services']['agent']
        self.assertEqual(actual['environment'], escaped['services']['agent']['environment'])
        self.assertEqual(actual['volumes'][0]['source'], escaped['services']['agent']['volumes'][0]['source'])
        # 官方 CLI 的标准化输出可直接重读，重复转义会改变密码、路径等值。
        again = subprocess.run(['docker', 'compose', '-p', 'installer-unit', '-f', '-', 'config', '--format', 'json'],
                               input=result.stdout, text=True, capture_output=True, check=True)
        self.assertEqual(json.loads(again.stdout), json.loads(result.stdout))

    def test_archive_reads_json_without_extracting_files(self):
        directory = ROOT / '.agent-temp' / 'installer-unit'
        directory.mkdir(parents=True, exist_ok=True)
        archive = directory / 'data.tar'
        with tarfile.open(archive, 'w') as output:
            content = b'{"serverId":"preserved","tokens":[]}'
            member = tarfile.TarInfo('./tokens.json')
            member.size = len(content)
            output.addfile(member, io.BytesIO(content))
        self.assertEqual(namespace['identity_from_archive'](archive)['tokens.json']['serverId'], 'preserved')

    def test_truncated_download_does_not_execute(self):
        result = subprocess.run(['bash'], input=(ROOT / 'install.sh').read_text().rsplit('\nPY\n', 1)[0],
                                text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('Docker 架构', result.stdout)

    def test_compose_drift_is_rejected_before_service_mutation(self):
        path = ROOT / 'deploy' / 'compose' / 'fnos.yml'
        container = {'Config': {'Labels': {'com.docker.compose.project': 'legacy',
                    'com.docker.compose.service': 'itemory-agent',
                    'com.docker.compose.project.working_dir': str(ROOT),
                    'com.docker.compose.project.config_files': str(path),
                    'com.docker.compose.config-hash': 'old'}}}
        with patch.dict(namespace, run=lambda cmd: 'itemory-agent changed' if '--hash' in cmd else '{}'):
            with self.assertRaisesRegex(namespace['InstallError'], '不一致'):
                namespace['old_configuration'](container)

    def archive_fixture(self):
        directory = ROOT / '.agent-temp' / ('oss-unit-' + uuid.uuid4().hex[:8])
        directory.mkdir(parents=True)
        payload = b'docker archive transport fixture'
        import hashlib
        checksum = hashlib.sha256(payload).hexdigest()
        image_id = 'sha256:' + 'a' * 64
        manifest = {'schema': 1, 'version': '0.5.0', 'repository': namespace['REPOSITORY'], 'images': {
            'arm64': {'file': 'itemory-agent-0.5.0-arm64.tar.gz', 'sha256': checksum,
                      'size': len(payload), 'image_id': image_id}}}
        args = SimpleNamespace(version='1', download_base='https://example.com/releases', download_dir=str(directory))
        return args, manifest, payload, image_id

    def test_oss_corrupt_download_is_rejected_before_docker_load(self):
        args, manifest, payload, _ = self.archive_fixture()
        def transport(url, destination, max_bytes=None):
            destination.write_bytes(json.dumps(manifest).encode() if url.endswith('.json') else payload + b'bad')
        with patch.dict(namespace, download=transport, docker=lambda *args, **kwargs: self.fail('must not import corrupt archive')):
            with self.assertRaisesRegex(namespace['InstallError'], 'SHA-256'):
                namespace['resolve_archive_image'](args, 'aarch64')

    def test_oss_checks_platform_and_pins_loaded_image_id(self):
        args, manifest, payload, image_id = self.archive_fixture()
        calls = []
        def transport(url, destination, max_bytes=None):
            destination.write_bytes(json.dumps(manifest).encode() if url.endswith('.json') else payload)
        metadata = {'Id': image_id, 'Os': 'linux', 'Architecture': 'arm64', 'Config': {'Labels': {
            'org.opencontainers.image.source': 'https://github.com/SherlockGougou/itemory-agent'}}}
        def docker(*args, **kwargs):
            calls.append(args)
            return json.dumps([metadata]) if args[:2] == ('image', 'inspect') else ''
        with patch.dict(namespace, download=transport, docker=docker):
            self.assertEqual(namespace['resolve_archive_image'](args, 'aarch64'), image_id)
            self.assertFalse(any(call[0] == 'pull' for call in calls))
            metadata['Architecture'] = 'amd64'
            with self.assertRaisesRegex(namespace['InstallError'], '平台'):
                namespace['resolve_archive_image'](args, 'aarch64')

    def test_oss_manifest_cannot_choose_another_repository_or_path(self):
        for change in ('repository', 'file', 'version'):
            args, manifest, _, _ = self.archive_fixture()
            if change == 'repository':
                manifest['repository'] = 'untrusted/image'
            elif change == 'version':
                args.version = '0.5.1'
            else:
                manifest['images']['arm64']['file'] = '../../untrusted.tar.gz'
            with patch.dict(namespace, download=lambda url, path, max_bytes=None: path.write_text(json.dumps(manifest)),
                            docker=lambda *args, **kwargs: self.fail('must not import invalid manifest')):
                with self.assertRaises(namespace['InstallError']):
                    namespace['resolve_archive_image'](args, 'arm64')

    def test_loaded_official_image_id_is_recognized_for_next_upgrade(self):
        image_id = 'sha256:' + 'a' * 64
        container = {'Image': image_id, 'Config': {'Image': image_id, 'Labels': {
            'org.opencontainers.image.source': 'https://github.com/SherlockGougou/itemory-agent'}}}
        self.assertTrue(namespace['official_container'](container))
        container['Config']['Labels'] = {}
        self.assertFalse(namespace['official_container'](container))

    def test_oss_download_failure_does_not_stop_existing_service(self):
        args = SimpleNamespace(container=None, version='1', photo_dir=[], user=None, port=None,
                               timezone=None, install_dir=None, yes=True, source='oss', architecture='arm64')
        existing = {'Id': 'running-original', 'Name': '/original'}
        def fail_download(*args):
            raise namespace['InstallError']('download failed')
        with patch.dict(namespace, find_existing=lambda name: existing,
                        old_configuration=lambda container: ({}, 'project', 'agent', str(ROOT)),
                        resolve_archive_image=fail_download,
                        docker=lambda *args, **kwargs: self.fail('must not stop or replace existing service')):
            with self.assertRaisesRegex(namespace['InstallError'], 'download failed'):
                namespace['execute'](args)


if __name__ == '__main__':
    unittest.main()
