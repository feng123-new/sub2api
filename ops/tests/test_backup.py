import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('backup_state', Path(__file__).parents[1] / 'lib/backup_state.py')
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


class BackupIntegrityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'root'
        self.dest = Path(self.temp.name) / 'backup'
        (self.root / 'runtime').mkdir(parents=True)
        (self.root / 'data/generated-images').mkdir(parents=True)
        self.dest.mkdir()

    def test_exact_release_is_self_contained(self):
        binary = self.root / 'runtime/sub2api'
        binary.write_bytes(b'release-binary')
        source = self.root / 'source.tar.gz'
        source.write_bytes(b'release-source')
        manifest = {'version': 'test', 'sha256': backup.sha256(binary),
                    'source_archive': str(source), 'source_archive_sha256': backup.sha256(source)}
        (self.root / 'runtime/manifest.json').write_text(json.dumps(manifest))
        backup.copy_release(self.root, self.dest)
        source.unlink()
        self.assertEqual((self.dest / 'runtime/source.tar.gz').read_bytes(), b'release-source')
        self.assertEqual(backup.sha256(self.dest / 'runtime/sub2api'), manifest['sha256'])

    def test_release_mismatch_is_rejected(self):
        (self.root / 'runtime/sub2api').write_bytes(b'wrong-release')
        (self.root / 'runtime/manifest.json').write_text(json.dumps({'sha256': '0' * 64}))
        with self.assertRaisesRegex(ValueError, 'Checksum mismatch'):
            backup.copy_release(self.root, self.dest)

    def image(self):
        content = b'archived-image'
        row = {'id': 'a' * 32, 'mime_type': 'image/png', 'byte_size': len(content),
               'sha256': hashlib.sha256(content).hexdigest()}
        path = self.root / 'data/generated-images' / (row['id'] + '.png')
        path.write_bytes(content)
        return row, path

    def test_snapshot_images_survive_source_deletion(self):
        row, path = self.image()
        index = backup.copy_images(self.root, self.dest, [row])
        path.unlink()
        self.assertEqual(index[0]['filename'], row['id'] + '.png')
        self.assertEqual(backup.sha256(self.dest / 'data/generated-images' / index[0]['filename']), row['sha256'])

    def test_corrupt_image_is_rejected(self):
        row, path = self.image()
        path.write_bytes(b'corruption')
        with self.assertRaisesRegex(ValueError, 'Checksum mismatch'):
            backup.copy_images(self.root, self.dest, [row])

    def test_image_deleted_before_copy_fails_closed(self):
        row, path = self.image()
        path.unlink()
        with self.assertRaises(FileNotFoundError):
            backup.copy_images(self.root, self.dest, [row])

    def test_invalid_image_path_is_rejected(self):
        row, _ = self.image()
        row['id'] = '../outside'
        with self.assertRaisesRegex(ValueError, 'Invalid archived image'):
            backup.copy_images(self.root, self.dest, [row])


if __name__ == '__main__':
    unittest.main()
