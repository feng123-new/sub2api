import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

OPS = Path(__file__).resolve().parents[1]
IMAGE_ID = 'sha256:' + 'a' * 64
IMAGE_REF = 'sub2api-codex-transport-rs:test'
IMAGE_ARCHIVE = 'runtime/codex-transport-rs-image.tar'
OVERLAY = 'compose/docker-compose.codex-rust.yml'


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


backup = load_module('deployment_backup_state', OPS / 'lib/backup_state.py')
verifier = load_module('deployment_verifier', OPS / 'verify-backup.py')


def deployment():
    return {
        'project': 'sub2api', 'project_directory': '.', 'environment_file': '.env',
        'compose_files': ['docker-compose.yml', OVERLAY],
        'codex_transport': {
            'service': 'codex-transport-rs', 'profile': 'codex-rust',
            'image_ref': IMAGE_REF, 'image_id': IMAGE_ID, 'repo_digests': [],
            'image_archive': IMAGE_ARCHIVE,
            'source_reference': 'runtime/source.tar.gz:codex-transport-rs',
        },
    }


class DeploymentCaptureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'root'
        self.dest = Path(self.temp.name) / 'backup'
        (self.root / 'compose').mkdir(parents=True)
        (self.dest / 'runtime').mkdir(parents=True)
        for name in ['.env', 'docker-compose.yml', OVERLAY]:
            (self.root / name).write_text('fixture:' + name)
        self.labels = {
            'com.docker.compose.project': 'sub2api',
            'com.docker.compose.project.working_dir': str(self.root),
            'com.docker.compose.project.config_files': ','.join(
                str(self.root / name) for name in ['docker-compose.yml', OVERLAY]),
        }
        self.config = {'services': {'codex-transport-rs': {
            'profiles': ['codex-rust'], 'image': IMAGE_REF}}}
        self.matches = 'sidecar-id\n'
        self.container = {'State': {'Running': True}, 'Config': {'Image': IMAGE_REF},
                          'Image': IMAGE_ID}
        self.image = {'Id': IMAGE_ID, 'RepoDigests': ['local@sha256:' + 'b' * 64]}
        self.save_mode = 'ok'
        self.commands = []
        self.runner = mock.patch.object(backup.subprocess, 'run', side_effect=self.docker)
        self.runner.start()
        self.addCleanup(self.runner.stop)

    def docker(self, command, **kwargs):
        self.commands.append(command)
        self.assertTrue(kwargs.get('check'), command)
        self.assertEqual(command[0], 'docker')
        if command == ['docker', 'inspect', 'sub2api']:
            output = json.dumps([{'Config': {'Labels': self.labels}}])
        elif command[1] == 'compose':
            output = json.dumps(self.config)
        elif command[1] == 'ps':
            self.assertIn('--all', command)
            self.assertIn('label=com.docker.compose.project=sub2api', command)
            self.assertIn('label=com.docker.compose.service=codex-transport-rs', command)
            output = self.matches
        elif command[:2] == ['docker', 'inspect']:
            self.assertEqual(command[2:], ['sidecar-id'])
            output = json.dumps([self.container])
        elif command[:3] == ['docker', 'image', 'inspect']:
            self.assertEqual(command[3:], [IMAGE_ID])
            output = json.dumps([self.image])
        elif command[:3] == ['docker', 'image', 'save']:
            self.assertEqual(command[3:], ['--output', str(self.dest / IMAGE_ARCHIVE), IMAGE_ID])
            if self.save_mode == 'error':
                raise subprocess.CalledProcessError(1, command, stderr='private error')
            (self.dest / IMAGE_ARCHIVE).write_bytes(b'image-tar' if self.save_mode == 'ok' else b'')
            output = ''
        else:
            self.fail('Unexpected Docker command: ' + repr(command))
        return subprocess.CompletedProcess(command, 0, stdout=output, stderr='')

    def capture(self):
        backup.copy_deployment(self.root, self.dest)
        return json.loads((self.dest / 'runtime/deployment.json').read_text())

    def test_ordered_overlay_and_immutable_image_are_captured(self):
        result = self.capture()
        expected = deployment()
        expected['codex_transport']['repo_digests'] = self.image['RepoDigests']
        self.assertEqual(result, expected)
        compose = next(command for command in self.commands if command[1] == 'compose')
        self.assertEqual(compose, [
            'docker', 'compose', '--project-directory', str(self.root),
            '--project-name', 'sub2api', '--env-file', str(self.root / '.env'),
            '-f', str(self.root / 'docker-compose.yml'), '-f', str(self.root / OVERLAY),
            '--profile', '*', 'config', '--format', 'json'])
        for name in result['compose_files']:
            self.assertEqual((self.dest / name).read_bytes(), (self.root / name).read_bytes())
        self.assertEqual((self.dest / IMAGE_ARCHIVE).read_bytes(), b'image-tar')

    def test_missing_repo_digests_are_optional(self):
        self.image.pop('RepoDigests')
        self.assertEqual(self.capture()['codex_transport']['repo_digests'], [])

    def test_relative_config_paths_preserve_order(self):
        self.labels['com.docker.compose.project.config_files'] = OVERLAY + ',docker-compose.yml'
        self.assertEqual(self.capture()['compose_files'], [OVERLAY, 'docker-compose.yml'])

    def test_missing_configured_sidecar_or_profile_is_rejected(self):
        for config in [{'services': {}}, {'services': {'codex-transport-rs': {
                'image': IMAGE_REF, 'profiles': ['other']}}}]:
            with self.subTest(config=config):
                self.config = config
                with self.assertRaisesRegex(ValueError, 'codex'):
                    self.capture()

    def test_missing_stopped_or_duplicate_sidecar_is_rejected(self):
        for matches, running in [('', True), ('sidecar-id\n', False),
                                 ('sidecar-id\nsecond-id\n', True)]:
            with self.subTest(matches=matches, running=running):
                self.matches = matches
                self.container['State']['Running'] = running
                with self.assertRaisesRegex(ValueError, 'codex'):
                    self.capture()

    def test_image_reference_mismatch_is_rejected(self):
        self.container['Config']['Image'] = 'unexpected:tag'
        with self.assertRaisesRegex(ValueError, 'image'):
            self.capture()

    def test_malformed_image_id_is_rejected(self):
        self.container['Image'] = 'mutable:tag'
        with self.assertRaisesRegex(ValueError, 'image'):
            self.capture()

    def test_foreign_working_directory_is_rejected(self):
        self.labels['com.docker.compose.project.working_dir'] = self.temp.name
        with self.assertRaisesRegex(ValueError, 'directory'):
            self.capture()

    def test_config_path_escape_and_symlink_escape_are_rejected(self):
        outside = Path(self.temp.name) / 'outside.yml'
        outside.write_text('private fixture')
        (self.root / 'escape.yml').symlink_to(outside)
        for path in [str(outside), '../outside.yml', 'escape.yml']:
            with self.subTest(path=path):
                self.labels['com.docker.compose.project.config_files'] = 'docker-compose.yml,' + path
                with self.assertRaisesRegex(ValueError, 'path'):
                    self.capture()

    def test_main_config_and_unique_paths_are_required(self):
        for paths in [OVERLAY, 'docker-compose.yml,docker-compose.yml']:
            with self.subTest(paths=paths):
                self.labels['com.docker.compose.project.config_files'] = paths
                with self.assertRaises(ValueError):
                    self.capture()

    def test_image_save_failure_or_empty_output_prevents_metadata(self):
        for mode in ['error', 'empty']:
            with self.subTest(mode=mode):
                self.save_mode = mode
                with self.assertRaises((ValueError, RuntimeError, subprocess.CalledProcessError)):
                    self.capture()
                self.assertFalse((self.dest / 'runtime/deployment.json').exists())


class DeploymentVerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.archive = self.root / 'backup.tar.gz'
        self.deployment = deployment()
        self.files = {
            '.env': b'fixture-secret', 'docker-compose.yml': b'main', OVERLAY: b'overlay',
            'postgres.dump': b'dump', 'runtime/sub2api': b'binary',
            'runtime/source.tar.gz': b'source', IMAGE_ARCHIVE: b'image-tar',
        }
        manifest = {'version': 'test', 'source_commit': 'test-commit',
                    'sha256': hashlib.sha256(b'binary').hexdigest(),
                    'source_archive_sha256': hashlib.sha256(b'source').hexdigest()}
        self.files['runtime/manifest.json'] = json.dumps(manifest).encode()

    def make_archive(self, format_version=3, omit_tar=(), omit_coverage=(),
                     nonregular=None, duplicate=None, corrupt=None):
        files = dict(self.files)
        if format_version == 3:
            files['runtime/deployment.json'] = json.dumps(self.deployment).encode()
        index = {name: hashlib.sha256(data).hexdigest() for name, data in files.items()
                 if name not in omit_coverage}
        files['backup.json'] = json.dumps({'format': format_version, 'files': index,
                                           'image_index': []}).encode()
        if corrupt is not None:
            files[corrupt] = b'corrupted'
        with tarfile.open(self.archive, 'w:gz') as output:
            for name, data in files.items():
                if name in omit_tar:
                    continue
                member = tarfile.TarInfo('snapshot/' + name)
                if nonregular and name == nonregular[0]:
                    member.type = nonregular[1]
                    member.linkname = 'snapshot/docker-compose.yml'
                    output.addfile(member)
                else:
                    member.size = len(data)
                    output.addfile(member, io.BytesIO(data))
                if name == duplicate:
                    output.addfile(member, io.BytesIO(data) if member.isfile() else None)
        return self.archive

    def test_format3_reports_deployment_coverage(self):
        result = verifier.verify(self.make_archive())
        self.assertTrue(result['valid'])
        self.assertTrue(result['deployment_covered'])
        self.assertTrue(result['codex_image_included'])
        self.assertEqual(result['format'], 3)

    def test_required_artifacts_cannot_be_omitted_from_tar_or_index(self):
        for name in ['runtime/deployment.json', '.env', 'docker-compose.yml', OVERLAY, IMAGE_ARCHIVE]:
            for omit_tar, omit_coverage in [([name], []), ([], [name]), ([name], [name])]:
                with self.subTest(name=name, tar=omit_tar, coverage=omit_coverage):
                    with self.assertRaises(ValueError):
                        verifier.verify(self.make_archive(omit_tar=omit_tar, omit_coverage=omit_coverage))

    def test_required_members_must_be_regular_and_unique(self):
        for name in ['runtime/deployment.json', '.env', 'docker-compose.yml', OVERLAY, IMAGE_ARCHIVE]:
            for kind in [tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.DIRTYPE, tarfile.FIFOTYPE]:
                with self.subTest(name=name, kind=kind):
                    with self.assertRaises(ValueError):
                        verifier.verify(self.make_archive(nonregular=(name, kind)))
            with self.subTest(duplicate=name):
                with self.assertRaises(ValueError):
                    verifier.verify(self.make_archive(duplicate=name))

    def test_regular_plus_symlink_duplicate_is_rejected(self):
        self.make_archive()
        with tarfile.open(self.archive, 'r:gz') as source:
            members = [(member, source.extractfile(member).read()) for member in source]
        with tarfile.open(self.archive, 'w:gz') as output:
            for member, data in members:
                output.addfile(member, io.BytesIO(data))
            extra = tarfile.TarInfo('snapshot/' + OVERLAY)
            extra.type = tarfile.SYMTYPE
            extra.linkname = 'docker-compose.yml'
            output.addfile(extra)
        with self.assertRaises(ValueError):
            verifier.verify(self.archive)

    def test_required_artifact_hashes_are_checked(self):
        for name in [OVERLAY, IMAGE_ARCHIVE]:
            with self.subTest(name=name):
                with self.assertRaisesRegex(ValueError, 'corrupt'):
                    verifier.verify(self.make_archive(corrupt=name))

    def test_deployment_metadata_validation(self):
        mutations = [
            ('project', ''), ('project_directory', '/outside'), ('environment_file', '../.env'),
            ('compose_files', []), ('compose_files', [OVERLAY]),
            ('compose_files', ['docker-compose.yml', OVERLAY, OVERLAY]),
            ('compose_files', ['docker-compose.yml', '../outside.yml']),
            ('compose_files', ['docker-compose.yml', '/outside.yml']),
            ('compose_files', ['docker-compose.yml', './alias.yml']),
            ('compose_files', ['docker-compose.yml', 'compose//alias.yml']),
            ('compose_files', ['docker-compose.yml', IMAGE_ARCHIVE]),
            ('codex_transport', None),
        ]
        for field, value in mutations:
            with self.subTest(field=field, value=value):
                self.deployment = deployment()
                self.deployment[field] = value
                with self.assertRaises(ValueError):
                    verifier.verify(self.make_archive())
        for field, value in [('service', 'other'), ('profile', 'other'), ('image_ref', ''),
                             ('image_id', 'tag'), ('image_archive', '../image.tar'),
                             ('repo_digests', 'not-a-list'), ('source_reference', 'unrelated')]:
            with self.subTest(field=field, value=value):
                self.deployment = deployment()
                self.deployment['codex_transport'][field] = value
                with self.assertRaises(ValueError):
                    verifier.verify(self.make_archive())

    def test_absent_malformed_or_incomplete_deployment_is_rejected(self):
        for value in [None, [], {}, {'project': 'sub2api'}]:
            with self.subTest(value=value):
                self.deployment = value
                with self.assertRaises(ValueError):
                    verifier.verify(self.make_archive())
        with self.assertRaises(ValueError):
            verifier.verify(self.make_archive(corrupt='runtime/deployment.json'))

    def test_legacy2_integrity_is_accepted_with_explicit_limit(self):
        result = verifier.verify(self.make_archive(format_version=2))
        self.assertTrue(result['valid'])
        self.assertEqual(result['format'], 2)
        self.assertFalse(result['deployment_covered'])
        self.assertFalse(result['codex_image_included'])
        with self.assertRaisesRegex(ValueError, 'deployment'):
            verifier.verify(self.archive, require_deployment=True)
        with self.assertRaisesRegex(ValueError, 'corrupt'):
            verifier.verify(self.make_archive(format_version=2, corrupt='postgres.dump'))

    def test_cli_coverage_gate_help_and_bad_input(self):
        script = str(OPS / 'verify-backup.py')
        valid = subprocess.run([sys.executable, script, '--require-deployment', str(self.make_archive())],
                               capture_output=True, text=True)
        self.assertEqual(valid.returncode, 0, valid.stderr)
        self.assertTrue(json.loads(valid.stdout)['deployment_covered'])
        self.make_archive(format_version=2)
        legacy = subprocess.run([sys.executable, script, str(self.archive)], capture_output=True, text=True)
        self.assertEqual(legacy.returncode, 0, legacy.stderr)
        self.assertFalse(json.loads(legacy.stdout)['deployment_covered'])
        strict = subprocess.run([sys.executable, script, '--require-deployment', str(self.archive)],
                                capture_output=True, text=True)
        self.assertNotEqual(strict.returncode, 0)
        self.assertIn('deployment', strict.stderr)
        help_result = subprocess.run([sys.executable, script, '--help'], capture_output=True, text=True)
        self.assertEqual(help_result.returncode, 0)
        self.assertIn('--require-deployment', help_result.stdout)
        self.archive.write_bytes(b'invalid tar')
        bad = subprocess.run([sys.executable, script, str(self.archive)], capture_output=True, text=True)
        self.assertNotEqual(bad.returncode, 0)
        self.assertNotIn('fixture-secret', valid.stdout + valid.stderr + bad.stdout + bad.stderr)


FAKE_DOCKER = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
args = sys.argv[1:]
root = Path(os.environ['FAKE_DEPLOY_ROOT'])
mode = os.environ['FAKE_DOCKER_MODE']
image_id = 'sha256:' + 'a' * 64
image_ref = 'sub2api-codex-transport-rs:test'
with Path(os.environ['FAKE_DOCKER_LOG']).open('a') as log:
    log.write(json.dumps(args) + '\n')
if args[:1] == ['compose']:
    if args[-1] == 'ps':
        sys.exit(0)
    assert args[-3:] == ['config', '--format', 'json']
    print(json.dumps({'services': {'codex-transport-rs': {
        'profiles': ['codex-rust'], 'image': image_ref}}}))
elif args == ['inspect', 'sub2api']:
    print(json.dumps([{'Config': {'Labels': {
        'com.docker.compose.project': 'sub2api',
        'com.docker.compose.project.working_dir': str(root),
        'com.docker.compose.project.config_files': str(root / 'docker-compose.yml') + ',' +
            str(root / 'compose/docker-compose.codex-rust.yml'),
    }}}]))
elif args == ['inspect', 'sidecar-id']:
    print(json.dumps([{'State': {'Running': True}, 'Config': {'Image': image_ref}, 'Image': image_id}]))
elif args[:1] == ['inspect']:
    assert args == ['inspect', 'sub2api', 'sub2api-postgres', 'sub2api-redis']
    print('[]')
elif args[:1] == ['ps']:
    if mode != 'absent':
        print('sidecar-id')
elif args[:2] == ['image', 'inspect']:
    assert args[2:] == [image_id]
    print(json.dumps([{'Id': image_id, 'RepoDigests': []}]))
elif args[:2] == ['image', 'save']:
    assert args[2] == '--output' and args[4] == image_id
    Path(args[3]).write_bytes(b'partial' if mode == 'save-failure' else b'image-tar')
    if mode == 'save-failure':
        sys.exit(23)
elif args[:1] == ['exec'] and 'sub2api-redis' in args:
    sys.exit(1)
elif args[:1] == ['cp']:
    assert args[1].startswith('sub2api-redis:')
    sys.exit(1)
elif args[:1] == ['exec'] and 'psql' in args:
    if '-c' not in args:
        for line in sys.stdin:
            if 'pg_export_snapshot' in line:
                print('snapshot-test', flush=True)
            elif 'generated_images' in line:
                print('[]', flush=True)
            elif 'schema_migrations' in line:
                print('305', flush=True)
            elif '\q' in line:
                break
elif args[:1] == ['exec'] and 'pg_dump' in args:
    print('fixture-dump')
elif args[:1] == ['exec'] and 'pg_restore' in args:
    sys.stdin.buffer.read()
    print('fixture-toc')
else:
    raise SystemExit('Unexpected fake Docker invocation')
'''


class BackupPublicationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / 'deploy'
        self.output = self.base / 'backups'
        self.fake_bin = self.base / 'bin'
        for path in [self.root / 'runtime', self.root / 'compose', self.output, self.fake_bin]:
            path.mkdir(parents=True)
        self.scripts = self.root / 'ops'
        self.scripts.mkdir()
        (self.scripts / 'lib').mkdir()
        for name in ['backup-sub2api.sh', 'verify-backup.py', 'guarded-docker.py', 'probe.py',
                     'README.md', 'lib/backup_state.py']:
            shutil.copy2(OPS / name, self.scripts / name)
        shell = self.scripts / 'backup-sub2api.sh'
        source = shell.read_text()
        for original, replacement in [
            ('DEPLOY_DIR=/opt/sub2api-deploy', 'DEPLOY_DIR=' + str(self.root)),
            ('BACKUP_ROOT=/home/fengyong/backups/sub2api-scheduled', 'BACKUP_ROOT=' + str(self.output)),
            ('PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
             'PATH=' + str(self.fake_bin) + ':/usr/local/bin:/usr/bin:/bin'),
        ]:
            self.assertEqual(source.count(original), 1)
            source = source.replace(original, replacement)
        shell.write_text(source)
        for name, content in [('docker', FAKE_DOCKER), ('curl', '#!/bin/sh\nprintf "{}\\n"\n')]:
            executable = self.fake_bin / name
            executable.write_text(content)
            executable.chmod(0o755)
        for name in ['.env', 'docker-compose.yml', OVERLAY]:
            (self.root / name).write_text('fixture:' + name)
        (self.root / 'runtime/sub2api').write_bytes(b'binary')
        (self.root / 'source.tar.gz').write_bytes(b'source')
        (self.root / 'runtime/manifest.json').write_text(json.dumps({
            'version': 'test', 'source_commit': 'test-commit',
            'sha256': hashlib.sha256(b'binary').hexdigest(),
            'source_archive': str(self.root / 'source.tar.gz'),
            'source_archive_sha256': hashlib.sha256(b'source').hexdigest(),
        }))
        (self.output / 'previous.tar.gz').write_bytes(b'previous-backup')
        (self.output / 'latest.tar.gz').symlink_to('previous.tar.gz')
        self.log = self.base / 'docker.jsonl'

    def invoke(self, mode):
        result = subprocess.run(['bash', str(self.scripts / 'backup-sub2api.sh')],
                                env={**os.environ, 'FAKE_DEPLOY_ROOT': str(self.root),
                                     'FAKE_DOCKER_MODE': mode, 'FAKE_DOCKER_LOG': str(self.log)},
                                capture_output=True, text=True, timeout=20)
        self.assertNotIn('fixture:.env', result.stdout + result.stderr)
        return result

    def test_failure_preserves_latest_and_never_opens_database_snapshot(self):
        for mode in ['absent', 'save-failure']:
            with self.subTest(mode=mode):
                self.log.write_text('')
                result = self.invoke(mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(os.readlink(self.output / 'latest.tar.gz'), 'previous.tar.gz')
                self.assertEqual((self.output / 'latest.tar.gz').read_bytes(), b'previous-backup')
                self.assertEqual(sorted(path.name for path in self.output.iterdir()),
                                 ['backup.log', 'latest.tar.gz', 'previous.tar.gz'])
                commands = [json.loads(line) for line in self.log.read_text().splitlines()]
                self.assertFalse(any('psql' in command and '-i' in command for command in commands))

    def test_complete_fake_cli_publishes_format3_with_revision(self):
        (self.scripts / 'REVISION').write_text('fixture-installed-revision\n')
        result = self.invoke('ok')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        latest = self.output / 'latest.tar.gz'
        validated = verifier.verify(latest)
        self.assertEqual(validated['format'], 3)
        self.assertTrue(validated['deployment_covered'])
        with tarfile.open(latest, 'r:gz') as archive:
            members = {member.name.split('/', 1)[1]: member for member in archive if '/' in member.name}
            self.assertEqual(archive.extractfile(members['ops/REVISION']).read(), b'fixture-installed-revision\n')
            metadata = json.load(archive.extractfile(members['backup.json']))
            self.assertIn(OVERLAY, metadata['files'])
            self.assertIn(IMAGE_ARCHIVE, metadata['files'])
            self.assertIn('runtime/deployment.json', metadata['files'])
        self.assertTrue(Path(str(latest.resolve()) + '.sha256').is_file())
        self.assertFalse(list(self.output.glob('*.partial')))


if __name__ == '__main__':
    unittest.main()
