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
                    'Config': {'Image': namespace['REPOSITORY'] + ':1', 'Labels': {
                        'com.docker.compose.project': 'legacy',
                        'com.docker.compose.service': 'agent',
                        'com.docker.compose.project.working_dir': str(directory),
                        'com.docker.compose.project.config_files': str(directory / 'compose.json')}},
                    'State': {'Running': True}}
        args = SimpleNamespace(container=None, version='1', photo_dir=[], user=None, port=None,
                               timezone=None, install_dir=None, yes=True, source='ghcr',
                               migrate_existing=False)
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

    def test_legacy_migration_renames_old_container_before_compose_start(self):
        directory = ROOT / '.agent-temp' / ('installer-legacy-' + uuid.uuid4().hex[:8])
        directory.mkdir(parents=True)
        container = self.legacy_container()
        model, _, service, _ = namespace['legacy_configuration'](container)
        final = json.loads(json.dumps(container))
        final['Image'] = 'new-image-id'
        calls = []

        def fake_docker(*args, **kwargs):
            calls.append(args)
            if args[:2] == ('image', 'inspect'):
                return '[{"Id":"new-image-id"}]'
            return ''

        def fake_run(args, **kwargs):
            calls.append(tuple(args))
            if 'ps' in args and '-aq' in args:
                return 'new-container-id'
            return ''

        args = SimpleNamespace(container=None, version='1', photo_dir=[], user=None, port=None,
                               timezone=None, install_dir=None, yes=True, source='ghcr',
                               migrate_existing=True)
        replacements = dict(find_existing=lambda name: container, resolve_image=lambda version: 'new-digest',
                            legacy_configuration=lambda item: (model, 'itemory-test', service, str(directory)),
                            choose_legacy_migration=lambda *args: None,
                            confirm=lambda *args: None, check_record_directory=lambda *args: None,
                            health=lambda *args: None, verify_identity=lambda *args: None,
                            identity_from_archive=lambda path: {}, inspect=lambda name: final,
                            docker=fake_docker, run=fake_run,
                            subprocess=SimpleNamespace(
                                run=lambda *args, **kwargs: SimpleNamespace(returncode=0)))
        with patch.dict(namespace, **replacements):
            namespace['execute'](args)
        renames = [call for call in calls if call[:2] == ('rename', 'legacy-container-id')]
        self.assertEqual(len(renames), 1)
        self.assertIn('-before-migration-', renames[0][2])
        self.assertTrue(any('up' in call for call in calls))

    def test_identity_accepts_last_seen_but_rejects_lost_pairing(self):
        before = {'tokens.json': {'serverId': 'server', 'tokens': [{'id': 'phone', 'hash': 'secret-hash', 'lastSeen': 'old'}]},
                  'admin.json': {'username': 'admin', 'passwordHash': 'hash'},
                  'settings.json': {'libraries': [{'id': 'library', 'path': '/volumes/photos'}]}}
        current = json.loads(json.dumps(before))
        current['tokens.json']['tokens'][0]['lastSeen'] = 'new'
        with patch.dict(namespace, docker=lambda *args, **kwargs: json.dumps(current[args[-1].split('/')[-1]])):
            namespace['verify_identity']('container', before)
            current['tokens.json']['tokens'] = []
            with self.assertRaisesRegex(namespace['InstallError'], '配对'):
                namespace['verify_identity']('container', before)

    def test_settings_changes_are_not_silently_accepted(self):
        with patch.dict(namespace, docker=lambda *args, **kwargs: '{"libraries": []}'):
            with self.assertRaisesRegex(namespace['InstallError'], '设置'):
                namespace['verify_identity']('container', {'settings.json': {'libraries': [{'id': 'keep'}]}})

    def test_data_layout_cannot_silently_change(self):
        container = {'Config': {'Env': ['ITEMORY_CACHE_DIR=/other']},
                     'Mounts': [{'Destination': '/data', 'Type': 'bind', 'RW': True}]}
        with self.assertRaisesRegex(namespace['InstallError'], '/data'):
            namespace['data_mount'](container)

    def legacy_container(self):
        return {
            'Id': 'legacy-container-id', 'Name': '/itemory-agent', 'Image': 'sha256:old',
            'Created': '2026-09-23T00:00:00Z', 'State': {'Running': True, 'Status': 'running'},
            'Config': {'Image': 'itemory-agent:0.3.13-exif-metadata', 'User': '1000:1001',
                       'Env': ['PATH=/usr/bin', 'ITEMORY_CACHE_DIR=/data', 'TZ=Asia/Shanghai',
                               'ITEMORY_PRESET=balanced'],
                       'Entrypoint': ['itemory-agent'], 'Cmd': None, 'WorkingDir': '', 'Labels': {}},
            'HostConfig': {'RestartPolicy': {'Name': 'unless-stopped'}, 'ReadonlyRootfs': True,
                           'CapDrop': ['ALL'], 'SecurityOpt': ['no-new-privileges:true'],
                           'Tmpfs': {'/tmp': 'size=128m'}, 'Memory': 4294967296,
                           'NanoCpus': 1500000000, 'NetworkMode': 'default', 'StopTimeout': 30,
                           'IpcMode': 'private'},
            'NetworkSettings': {'Ports': {'8787/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '8787'}]}},
            'Mounts': [
                {'Type': 'bind', 'Source': '/vol1/1000/itemory-agent/data',
                 'Destination': '/data', 'RW': True},
                {'Type': 'bind', 'Source': '/vol1/1000/Photos',
                 'Destination': '/volumes/Photos', 'RW': False},
            ],
        }

    def test_legacy_migration_preserves_runtime_configuration(self):
        model, project, service, directory = namespace['legacy_configuration'](self.legacy_container())
        actual = model['services'][service]
        self.assertEqual(actual['container_name'], 'itemory-agent')
        self.assertEqual(actual['user'], '1000:1001')
        self.assertTrue(actual['read_only'])
        self.assertEqual(actual['cap_drop'], ['ALL'])
        self.assertEqual(actual['security_opt'], ['no-new-privileges:true'])
        self.assertEqual(actual['tmpfs'], ['/tmp:size=128m'])
        self.assertEqual(actual['mem_limit'], '4294967296')
        self.assertEqual(actual['cpus'], '1.5')
        self.assertEqual(actual['restart'], 'unless-stopped')
        self.assertEqual(actual['stop_grace_period'], '30s')
        self.assertEqual(actual['ports'][0]['published'], '8787')
        self.assertTrue(actual['volumes'][1].get('read_only'))
        self.assertEqual(directory, '/vol1/1000/itemory-agent')
        self.assertRegex(project, r'^itemory-[a-f0-9]{12}$')

    def test_duplicate_ipv4_ipv6_bindings_are_displayed_once(self):
        ports = [
            {'published': '8787', 'target': 8787, 'protocol': 'tcp'},
            {'published': '8787', 'target': 8787, 'protocol': 'tcp'},
        ]
        self.assertEqual(namespace['display_ports'](ports), '8787 -> 8787')
        actual = {'8787/tcp': [
            {'HostIp': '0.0.0.0', 'HostPort': '8787'},
            {'HostIp': '::', 'HostPort': '8787'},
        ]}
        self.assertEqual(namespace['management_urls'](actual), ['http://<NAS局域网IP>:8787'])

    def test_legacy_migration_choice_can_exit_without_changes(self):
        container = self.legacy_container()
        model, _, _, _ = namespace['legacy_configuration'](container)
        args = SimpleNamespace(yes=False, migrate_existing=False)
        with patch.dict(namespace, ask=lambda label, default='': '2'):
            with self.assertRaisesRegex(namespace['UserCancelled'], '未做任何修改'):
                namespace['choose_legacy_migration'](args, container, model)

    def test_yes_cannot_silently_migrate_a_legacy_install(self):
        container = self.legacy_container()
        model, _, _, _ = namespace['legacy_configuration'](container)
        with self.assertRaisesRegex(namespace['InstallError'], '--migrate-existing'):
            namespace['choose_legacy_migration'](
                SimpleNamespace(yes=True, migrate_existing=False), container, model)

    def test_legacy_migration_requires_an_explicit_flag(self):
        container = self.legacy_container()
        model, _, _, _ = namespace['legacy_configuration'](container)
        namespace['choose_legacy_migration'](
            SimpleNamespace(yes=True, migrate_existing=True), container, model)

    def test_migration_choice_accepts_numbers_and_reprompts(self):
        answers = iter(['9', '1'])
        with patch.dict(namespace, ask=lambda label, default='': next(answers)):
            self.assertEqual(namespace['choose'](
                '请输入选项编号', [('migrate', '迁移并继续'), ('exit', '退出')], 'migrate'),
                'migrate')

    def test_confirmation_uses_numbered_options(self):
        with patch.dict(namespace, ask=lambda label, default='': '1'):
            namespace['confirm'](SimpleNamespace(yes=False), '是否继续？')
        with patch.dict(namespace, ask=lambda label, default='': '2'):
            with self.assertRaises(namespace['UserCancelled']):
                namespace['confirm'](SimpleNamespace(yes=False), '是否继续？')

    def test_find_existing_keeps_legacy_itemory_but_rejects_unrelated_container(self):
        container = self.legacy_container()
        with patch.dict(namespace, docker=lambda *args, **kwargs:
                        'legacy-container-id' if args[:2] == ('ps', '-aq') else json.dumps([container])):
            self.assertEqual(namespace['find_existing'](None), container)
        container['Config']['Image'] = 'nginx:latest'
        with patch.dict(namespace, docker=lambda *args, **kwargs:
                        'legacy-container-id' if args[:2] == ('ps', '-aq') else json.dumps([container])):
            with self.assertRaisesRegex(namespace['InstallError'], '不是 Itemory 安装'):
                namespace['find_existing'](None)

    def test_official_image_without_compose_metadata_uses_migration_path(self):
        container = self.legacy_container()
        container['Config']['Image'] = namespace['REPOSITORY'] + ':1'
        self.assertTrue(namespace['official_container'](container))
        self.assertFalse(namespace['compose_managed'](container))

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
        with patch.dict(namespace, run=lambda cmd, **kwargs: 'itemory-agent changed' if '--hash' in cmd else '{}'):
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
        existing = {'Id': 'running-original', 'Name': '/original',
                    'Config': {'Image': namespace['REPOSITORY'] + ':1', 'Labels': {
                        'com.docker.compose.project': 'legacy',
                        'com.docker.compose.service': 'agent',
                        'com.docker.compose.project.working_dir': str(ROOT),
                        'com.docker.compose.project.config_files': str(ROOT / 'compose.json')}}}
        def fail_download(*args):
            raise namespace['InstallError']('download failed')
        with patch.dict(namespace, find_existing=lambda name: existing,
                        old_configuration=lambda container: ({}, 'project', 'agent', str(ROOT)),
                        resolve_archive_image=fail_download,
                        docker=lambda *args, **kwargs: self.fail('must not stop or replace existing service')):
            with self.assertRaisesRegex(namespace['InstallError'], 'download failed'):
                namespace['execute'](args)


class LoggingTests(unittest.TestCase):
    """日志要能在失败时被用户复制给我们，同时不能把凭据写进去。"""

    def setUp(self):
        self.directory = ROOT / '.agent-temp' / ('installer-log-' + uuid.uuid4().hex[:8])
        self.directory.mkdir(parents=True)
        self.previous = namespace['LOG']
        self.log = namespace['Logger']()
        namespace['LOG'] = self.log

    def tearDown(self):
        self.log.close()
        namespace['LOG'] = self.previous

    def test_log_file_is_created_private_with_every_stage(self):
        path = self.log.open(str(self.directory / 'install.log'))
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.log.stage_start(1, '检查运行环境')
        self.log.done('Python 3.12')
        self.log.close()
        text = path.read_text()
        self.assertIn('[STEP]', text)
        self.assertIn('[OK]', text)
        self.assertIn('检查运行环境', text)

    def test_credentials_are_redacted_before_reaching_the_file(self):
        path = self.log.open(str(self.directory / 'install.log'))
        self.log.error(namespace['redact']('Authorization: Bearer abc123\npassword=hunter2\ntoken=xyz'))
        self.log.close()
        text = path.read_text()
        for secret in ('abc123', 'hunter2', 'xyz'):
            self.assertNotIn(secret, text)
        self.assertIn('***', text)

    def test_multiline_output_gets_one_timestamp_per_line(self):
        path = self.log.open(str(self.directory / 'install.log'))
        self.log.error('第一行\n第二行\n第三行')
        self.log.close()
        lines = [line for line in path.read_text().splitlines() if line.strip()]
        self.assertEqual(len(lines), 3)
        self.assertTrue(all('[ERROR]' in line for line in lines))

    def test_failure_report_names_the_log_and_support_channel(self):
        path = self.log.open(str(self.directory / 'install.log'))
        self.log.stage = '获取镜像'
        with patch('sys.stderr', new_callable=io.StringIO) as stderr:
            namespace['report_failure'](namespace['InstallError']('下载失败（curl 错误编号 6）'))
        output = stderr.getvalue()
        self.assertIn(str(path), output)
        self.assertIn('tail -n 200', output)
        self.assertIn(namespace['SUPPORT_EMAIL'], output)
        self.assertIn('获取镜像', output)

    def test_network_and_permission_failures_get_actionable_hints(self):
        hints = namespace['failure_hints']
        self.assertTrue(any('权限' in hint for hint in hints('permission denied')))
        self.assertTrue(any('端口' in hint for hint in hints('port is already allocated')))
        self.assertTrue(any('--source oss' in hint for hint in hints('TLS handshake timeout')))
        self.assertTrue(any('空间' in hint for hint in hints('no space left on device')))
        self.assertEqual(hints('完全不同的错误'), [])

    def test_installer_never_sets_a_cpu_hard_limit(self):
        # cpus 是 Docker 按机器核数校验的硬上限：静态值 >1 会让单核 NAS 创建失败，
        # 写 1 又会让多核机器慢十几倍。所以安装器不设置它，靠 preset 的并发数控制。
        self.assertFalse(hasattr(namespace, 'cpu_limit'))
        source = (ROOT / 'install.sh').read_text()
        self.assertNotIn('"cpus":', source)
        self.assertIn('"ITEMORY_PRESET": "balanced"', source)

    def test_log_copy_lands_in_the_record_directory_after_a_failure(self):
        directory = self.directory / 'record'
        directory.mkdir()
        self.log.open(str(self.directory / 'install.log'))
        self.log.info('升级失败')
        self.log.close()
        target = namespace['save_log_copy'](directory)
        self.assertEqual(target, directory / 'install.log')
        self.assertEqual(target.stat().st_mode & 0o777, 0o600)
        self.assertIn('升级失败', target.read_text())

    def test_log_copy_never_creates_missing_directories(self):
        absent = self.directory / 'not-created'
        self.log.open(str(self.directory / 'install.log'))
        self.assertIsNone(namespace['save_log_copy'](absent))
        self.assertFalse(absent.exists())

    def test_debug_output_is_kept_in_the_file_without_the_flag(self):
        path = self.log.open(str(self.directory / 'install.log'))
        with patch('sys.stdout', new_callable=io.StringIO) as stdout:
            self.log.debug('执行：docker ps -aq')
        self.assertNotIn('docker ps -aq', stdout.getvalue())
        self.log.debug_enabled = True
        with patch('sys.stdout', new_callable=io.StringIO) as stdout:
            self.log.debug('执行：docker ps -aq')
        self.assertIn('docker ps -aq', stdout.getvalue())
        self.log.close()
        self.assertEqual(path.read_text().count('docker ps -aq'), 2)

    def test_cancelling_is_not_reported_as_a_failure(self):
        args = SimpleNamespace(yes=False)
        with patch.dict(namespace, ask=lambda label, default='': '2'):
            with self.assertRaises(namespace['UserCancelled']):
                namespace['confirm'](args, '将创建安装目录')
        # 取消走独立分支：不打印排查块，也不要求用户发日志。
        self.assertTrue(issubclass(namespace['UserCancelled'], namespace['InstallError']))

    def test_confirmation_accepts_an_explicit_yes(self):
        args = SimpleNamespace(yes=False)
        with patch.dict(namespace, ask=lambda label, default='': '1'):
            namespace['confirm'](args, '将创建安装目录')
        namespace['confirm'](SimpleNamespace(yes=True), '确认句话仍会打印')

    def test_unwritable_log_location_does_not_stop_the_install(self):
        blocker = self.directory / 'blocked'
        blocker.write_text('not a directory')
        with patch('sys.stderr', new_callable=io.StringIO):
            self.log.open(str(blocker / 'install.log'))
        self.assertIsNone(self.log.path)
        self.log.info('仍然继续')
        self.log.close()


if __name__ == '__main__':
    unittest.main()
