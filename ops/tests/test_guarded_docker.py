import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('guarded_docker', Path(__file__).parents[1] / 'guarded-docker.py')
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


class GuardResultTests(unittest.TestCase):
    def invoke(self, client_exit=0, status='exited', running=False, container_exit=0, extra=()):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = {'Status': status, 'Running': running, 'ExitCode': container_exit,
                     'OOMKilled': False, 'StartedAt': '2026-10-03T00:00:00Z' if status != 'created' else '0001-01-01T00:00:00Z'}
            process = mock.Mock()
            process.poll.return_value = client_exit
            process.wait.return_value = client_exit
            args = ['guarded-docker.py', '--evidence-dir', directory, '--', *extra, 'node:24-alpine', 'node', '--version']
            with mock.patch.object(guard, 'ROOT', root), \
                    mock.patch.object(guard, 'available_memory', return_value=4096), \
                    mock.patch.object(guard.signal, 'signal'), \
                    mock.patch.object(guard.sys, 'argv', args), \
                    mock.patch.object(guard.subprocess, 'Popen', return_value=process), \
                    mock.patch.object(guard.subprocess, 'check_output', side_effect=['container-id', json.dumps([{'State': state}])]), \
                    mock.patch.object(guard.subprocess, 'run'), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                return guard.main()

    def test_completed_success_is_success(self):
        self.assertEqual(self.invoke(), 0)

    def test_workload_failure_is_preserved(self):
        self.assertEqual(self.invoke(client_exit=7, container_exit=7), 7)

    def test_start_failure_is_not_success(self):
        self.assertNotEqual(self.invoke(client_exit=1, status='created'), 0)

    def test_attachment_failure_is_not_success(self):
        self.assertNotEqual(self.invoke(client_exit=1, status='running', running=True), 0)

    def test_unfinished_container_is_not_success(self):
        self.assertNotEqual(self.invoke(status='running', running=True), 0)

    def test_short_memory_flags_are_rejected(self):
        for flags in [('-m', '6g'), ('-m6g',), ('-m=6g',)]:
            with self.subTest(flags=flags):
                with self.assertRaises(SystemExit) as result:
                    self.invoke(extra=flags)
                self.assertEqual(result.exception.code, 2)


if __name__ == '__main__':
    unittest.main()
