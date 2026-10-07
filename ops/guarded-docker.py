#!/usr/bin/env python3
"""Run one resource-limited build container on the shared service VPS."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

ROOT = Path('/opt/sub2api-deploy')


def available_memory():
    return int(next(line.split()[1] for line in Path('/proc/meminfo').read_text().splitlines()
                    if line.startswith('MemAvailable:'))) // 1024


def interrupted(signum, frame):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence-dir', required=True, type=Path)
    parser.add_argument('--memory-mib', type=int, default=2800)
    parser.add_argument('--cpus', type=float, default=2)
    parser.add_argument('docker_args', nargs=argparse.REMAINDER,
                        help='after --: docker volume/env/workdir options, image and command')
    args = parser.parse_args()
    docker_args = args.docker_args[1:] if args.docker_args[:1] == ['--'] else args.docker_args
    if not docker_args or not args.evidence_dir.is_dir():
        parser.error('an existing evidence directory and a Docker command are required')
    if not 128 <= args.memory_mib <= 2800 or not 0 < args.cpus <= 2:
        parser.error('this VPS profile permits 128..2800 MiB and at most 2 CPUs')
    forbidden = ('--memory', '--cpus', '--name', '--restart', '--oom-score-adj', '--rm')
    if any(arg.split('=', 1)[0] in forbidden or arg.startswith('--memory-') or arg.startswith('-m')
           for arg in docker_args):
        parser.error('container identity, lifecycle and memory/CPU limits are owned by this runner')
    signal.signal(signal.SIGTERM, interrupted)
    with (ROOT / '.sub2api-build.lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            parser.error('another guarded build is running')
        minimum = available_memory()
        if minimum < 3500:
            parser.error('at least 3500 MiB MemAvailable is required to start')
        name = 'sub2api-build-' + str(os.getpid())
        cid = subprocess.check_output([
            'docker', 'create', '--name', name, '--restart=no', '--oom-score-adj=900',
            '--cpus=' + str(args.cpus), '--memory=' + str(args.memory_mib) + 'm',
            '--memory-swap=' + str(args.memory_mib) + 'm', *docker_args], text=True).strip()
        aborted = False
        try:
            process = subprocess.Popen(['docker', 'start', '-a', name])
            while process.poll() is None:
                time.sleep(2)
                available = available_memory()
                minimum = min(minimum, available)
                full = next(line for line in Path('/proc/pressure/memory').read_text().splitlines()
                            if line.startswith('full '))
                pressure = float(full.split('avg10=')[1].split()[0])
                if available < 1024 or pressure > 10:
                    aborted = True
                    print('BUILD GUARD STOP: memory/pressure threshold exceeded', file=sys.stderr, flush=True)
                    cgroup = Path('/sys/fs/cgroup/system.slice/docker-' + cid + '.scope/cgroup.kill')
                    if cgroup.exists():
                        subprocess.run(['sudo', '-n', 'tee', str(cgroup)], input='1\n', text=True,
                                       stdout=subprocess.DEVNULL, check=True)
                    subprocess.run(['docker', 'kill', name], stdout=subprocess.DEVNULL,
                                   stderr=subprocess.DEVNULL, timeout=15)
                    break
            client_exit = process.wait(timeout=30)
            state = json.loads(subprocess.check_output(['docker', 'inspect', name], text=True))[0]['State']
            completed = (state['Status'] == 'exited' and not state['Running']
                         and state.get('StartedAt') not in (None, '', '0001-01-01T00:00:00Z'))
            exit_code = state['ExitCode'] if completed else 125
            if client_exit != 0:
                exit_code = client_exit if client_exit > 0 else 125
            record = {'name': name, 'minimum_mem_available_mib': minimum,
                      'guard_aborted': aborted, 'exit': 125 if aborted else exit_code,
                      'container_exit': state['ExitCode'], 'docker_client_exit': client_exit,
                      'container_status': state['Status'], 'oom_killed': state['OOMKilled']}
            with (args.evidence_dir / 'resource-guards.jsonl').open('a') as output:
                output.write(json.dumps(record) + '\n')
            print(json.dumps(record), flush=True)
            return record['exit']
        finally:
            subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, timeout=30)


if __name__ == '__main__':
    sys.exit(main())
