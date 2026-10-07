#!/usr/bin/env python3
"""Capture release files and images belonging to the PostgreSQL dump snapshot."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess


def sha256(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def copy_checked(source, destination, expected):
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)
    if sha256(destination) != expected:
        raise ValueError('Checksum mismatch: ' + str(destination))


def copy_release(root, destination):
    manifest = json.loads((root / 'runtime/manifest.json').read_text())
    runtime = destination / 'runtime'
    runtime.mkdir()
    copy_checked(root / 'runtime/sub2api', runtime / 'sub2api', manifest['sha256'])
    copy_checked(Path(manifest['source_archive']), runtime / 'source.tar.gz',
                 manifest['source_archive_sha256'])
    shutil.copy2(root / 'runtime/manifest.json', runtime / 'manifest.json')
    return manifest


def copy_images(root, destination, rows):
    extensions = {'image/png': '.png', 'image/jpeg': '.jpg', 'image/webp': '.webp', 'image/gif': '.gif'}
    target = destination / 'data/generated-images'
    target.mkdir(parents=True)
    index = []
    for row in rows:
        if not re.fullmatch('[0-9a-f]{32}', row['id']) or row['mime_type'] not in extensions:
            raise ValueError('Invalid archived image identity or MIME type')
        filename = row['id'] + extensions[row['mime_type']]
        path = target / filename
        copy_checked(root / 'data/generated-images' / filename, path, row['sha256'])
        if path.stat().st_size != row['byte_size']:
            raise ValueError('Image byte size mismatch: ' + filename)
        index.append({**row, 'filename': filename})
    return index


def query(process, statement):
    process.stdin.write(statement + '\n')
    process.stdin.flush()
    line = process.stdout.readline()
    if not line:
        raise RuntimeError('PostgreSQL snapshot session closed unexpectedly')
    return line.strip()


def capture(root, destination):
    manifest = copy_release(root, destination)
    process = subprocess.Popen([
        'docker', 'exec', '-i', 'sub2api-postgres', 'psql', '-U', 'sub2api', '-d', 'sub2api',
        '-XqAt', '-v', 'ON_ERROR_STOP=1'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    try:
        snapshot = query(process, 'BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SELECT pg_export_snapshot();')
        rows = json.loads(query(process, "SELECT COALESCE(jsonb_agg(t), '[]'::jsonb) FROM "
                                '(SELECT id,mime_type,byte_size,sha256 FROM generated_images ORDER BY id) t;'))
        migrations = int(query(process, 'SELECT count(*) FROM schema_migrations;'))
        # Archive files are immutable once indexed. Copy and hash them while this
        # snapshot is alive. Concurrent deletion fails the backup, never latest.
        images = copy_images(root, destination, rows)
        with (destination / 'postgres.dump').open('wb') as output:
            subprocess.run(['docker', 'exec', 'sub2api-postgres', 'pg_dump', '-U', 'sub2api',
                            '-d', 'sub2api', '-Fc', '--snapshot=' + snapshot], stdout=output, check=True)
    finally:
        if process.poll() is None:
            try:
                process.communicate('ROLLBACK;\n\\q\n', timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise
    if process.returncode != 0:
        raise RuntimeError('PostgreSQL snapshot session failed')
    with (destination / 'postgres.dump').open('rb') as dump, (destination / 'postgres.contents').open('wb') as toc:
        subprocess.run(['docker', 'exec', '-i', 'sub2api-postgres', 'pg_restore', '--list'],
                       stdin=dump, stdout=toc, check=True)
    files = {str(path.relative_to(destination)): sha256(path)
             for path in destination.rglob('*') if path.is_file()}
    metadata = {'format': 2, 'version': manifest['version'], 'source_commit': manifest['source_commit'],
                'database_snapshot': snapshot, 'migrations': migrations, 'image_index': images,
                'files': files, 'restore_note': 'Images and database use one snapshot. Redis is supplemental; '
                'a compatible base image and PostgreSQL/Redis installation are required for recovery.'}
    (destination / 'backup.json').write_text(json.dumps(metadata, indent=2) + '\n')
    return {'version': manifest['version'], 'images': len(images), 'migrations': migrations,
            'verified_files': len(files)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('deploy_root', type=Path)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    print(json.dumps(capture(args.deploy_root, args.destination)))


if __name__ == '__main__':
    main()
