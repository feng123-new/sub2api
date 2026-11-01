#!/usr/bin/env python3
"""Stream-verify format-2/3 sub2api backups without extracting secrets."""
import argparse
import hashlib
import json
from pathlib import PurePosixPath
import re
import tarfile


def deployment_files(deployment):
    if not isinstance(deployment, dict):
        raise ValueError('Missing or malformed deployment metadata')
    project = deployment.get('project')
    if not isinstance(project, str) or not re.fullmatch('[a-z0-9][a-z0-9_-]*', project):
        raise ValueError('Invalid deployment project')
    if deployment.get('project_directory') != '.' or deployment.get('environment_file') != '.env':
        raise ValueError('Invalid deployment directory or environment file')
    configs = deployment.get('compose_files')
    if not isinstance(configs, list) or not configs:
        raise ValueError('Missing deployment Compose files')
    for name in configs:
        if (not isinstance(name, str) or not name or '\x00' in name or '\\' in name
                or PurePosixPath(name).is_absolute() or '..' in PurePosixPath(name).parts
                or PurePosixPath(name).as_posix() != name or name == '.'):
            raise ValueError('Unsafe deployment Compose path')
    if len(set(configs)) != len(configs) or 'docker-compose.yml' not in configs:
        raise ValueError('Duplicate or missing deployment Compose files')
    codex = deployment.get('codex_transport')
    if not isinstance(codex, dict):
        raise ValueError('Missing codex deployment metadata')
    if codex.get('service') != 'codex-transport-rs' or codex.get('profile') != 'codex-rust':
        raise ValueError('Invalid codex deployment service or profile')
    if not isinstance(codex.get('image_ref'), str) or not re.fullmatch(r'\S+', codex['image_ref']):
        raise ValueError('Invalid codex image reference')
    if not isinstance(codex.get('image_id'), str) or not re.fullmatch('sha256:[0-9a-f]{64}', codex['image_id']):
        raise ValueError('Invalid immutable codex image ID')
    digests = codex.get('repo_digests')
    if not isinstance(digests, list) or any(not isinstance(digest, str) or not digest for digest in digests):
        raise ValueError('Invalid codex image repository digests')
    image_archive = 'runtime/codex-transport-rs-image.tar'
    if (codex.get('image_archive') != image_archive
            or codex.get('source_reference') != 'runtime/source.tar.gz:codex-transport-rs'):
        raise ValueError('Invalid codex image archive or available-source reference')
    reserved = {'.env', 'backup.json', 'postgres.dump', 'runtime/manifest.json',
                'runtime/sub2api', 'runtime/source.tar.gz', 'runtime/deployment.json', image_archive}
    if reserved.intersection(configs):
        raise ValueError('Deployment Compose path collides with a required artifact')
    return {'runtime/deployment.json', image_archive, *configs}


def verify(path, require_deployment=False):
    hashes = {}
    metadata = {}
    roots = set()
    members = {}
    with tarfile.open(path, 'r|gz') as archive:
        for member in archive:
            parts = PurePosixPath(member.name).parts
            if not parts or member.name.startswith('/') or '..' in parts:
                raise ValueError('Unsafe archive member')
            roots.add(parts[0])
            name = '/'.join(parts[1:])
            members.setdefault(name, []).append(member.isfile())
            if not member.isfile():
                continue
            if name in hashes:
                raise ValueError('Duplicate archive member: ' + name)
            digest = hashlib.sha256()
            capture = bytearray() if name in ('backup.json', 'runtime/manifest.json', 'runtime/deployment.json') else None
            stream = archive.extractfile(member)
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
                if capture is not None:
                    capture.extend(chunk)
            hashes[name] = digest.hexdigest()
            if capture is not None:
                metadata[name] = capture
    if len(roots) != 1 or 'backup.json' not in metadata or 'runtime/manifest.json' not in metadata:
        raise ValueError('Backup lacks release manifest or coverage metadata')
    coverage = json.loads(metadata['backup.json'])
    manifest = json.loads(metadata['runtime/manifest.json'])
    format_version = coverage['format']
    if format_version not in (2, 3):
        raise ValueError('Unsupported backup format')
    if require_deployment and format_version != 3:
        raise ValueError('Format-3 deployment coverage is required; format 2 has limited coverage')
    required = {'postgres.dump', '.env', 'docker-compose.yml', 'runtime/manifest.json',
                'runtime/sub2api', 'runtime/source.tar.gz'}
    if format_version == 3:
        required.update(deployment_files(json.loads(metadata.get('runtime/deployment.json', b'null'))))
        for name in required | {'backup.json'}:
            if members.get(name) != [True]:
                raise ValueError('Required artifact must be one regular archive member: ' + name)
    if not required.issubset(coverage['files']):
        raise ValueError('Required recovery files are missing from coverage')
    for name, expected in coverage['files'].items():
        if hashes.get(name) != expected:
            raise ValueError('Missing or corrupt backup file: ' + name)
    if hashes['runtime/sub2api'] != manifest['sha256']:
        raise ValueError('Release binary differs from manifest')
    if hashes['runtime/source.tar.gz'] != manifest['source_archive_sha256']:
        raise ValueError('Release source differs from manifest')
    for image in coverage['image_index']:
        if hashes.get('data/generated-images/' + image['filename']) != image['sha256']:
            raise ValueError('Image index differs from archived image')
    return {'valid': True, 'version': manifest['version'], 'commit': manifest['source_commit'],
            'images': len(coverage['image_index']), 'verified_files': len(coverage['files']),
            'format': format_version, 'deployment_covered': format_version == 3,
            'codex_image_included': format_version == 3}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive')
    parser.add_argument('--require-deployment', action='store_true',
                        help='Reject legacy backups without format-3 deployment coverage')
    args = parser.parse_args()
    print(json.dumps(verify(args.archive, require_deployment=args.require_deployment), indent=2))


if __name__ == '__main__':
    main()
