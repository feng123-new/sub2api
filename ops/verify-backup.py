#!/usr/bin/env python3
"""Stream-verify a format-2 sub2api backup without extracting secrets."""
import argparse
import hashlib
import json
from pathlib import PurePosixPath
import tarfile


def verify(path):
    hashes = {}
    metadata = {}
    roots = set()
    with tarfile.open(path, 'r|gz') as archive:
        for member in archive:
            parts = PurePosixPath(member.name).parts
            if not parts or member.name.startswith('/') or '..' in parts:
                raise ValueError('Unsafe archive member')
            roots.add(parts[0])
            if not member.isfile():
                continue
            name = '/'.join(parts[1:])
            if name in hashes:
                raise ValueError('Duplicate archive member: ' + name)
            digest = hashlib.sha256()
            capture = bytearray() if name in ('backup.json', 'runtime/manifest.json') else None
            stream = archive.extractfile(member)
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
                if capture is not None:
                    capture.extend(chunk)
            hashes[name] = digest.hexdigest()
            if capture is not None:
                metadata[name] = json.loads(capture)
    if len(roots) != 1 or 'backup.json' not in metadata or 'runtime/manifest.json' not in metadata:
        raise ValueError('Backup lacks release manifest or format-2 coverage metadata')
    coverage = metadata['backup.json']
    manifest = metadata['runtime/manifest.json']
    if coverage['format'] != 2:
        raise ValueError('Unsupported backup format')
    required = {'postgres.dump', '.env', 'docker-compose.yml', 'runtime/manifest.json',
                'runtime/sub2api', 'runtime/source.tar.gz'}
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
            'images': len(coverage['image_index']), 'verified_files': len(coverage['files'])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive')
    args = parser.parse_args()
    print(json.dumps(verify(args.archive), indent=2))


if __name__ == '__main__':
    main()
