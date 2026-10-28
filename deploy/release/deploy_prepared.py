#!/usr/bin/env python3
"""Deploy a prepared immutable release under the common lock and baseline guard.

Artifact files and optional ipv6-pool.json are owner-only staged inputs. This
wrapper never builds on the server. Only maintenance_release controls app stop,
migration, route changes and rollback; network installation affects its own unit.
"""
import argparse
import contextlib
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tarfile
import tempfile

import maintenance_release as release
from ipv6_pool import validate_config

POOL_TARGETS = ('/opt/codex2api/ipv6_pool.py', '/opt/codex2api/ipv6-pool.json',
                '/etc/systemd/system/codex2api-ipv6-pool.service')


def verify_tools(root, manifest):
    for name, digest in manifest.get('release_tools_sha256', {}).items():
        if pathlib.Path(name).name != name or not isinstance(digest, str):
            raise ValueError('invalid release tool manifest')
        path = root / name
        if path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError('staged release tool differs from verified source')


def atomic_install(path, data, mode):
    path = pathlib.Path(path)
    if path.is_symlink():
        raise ValueError('installation symlink refused')
    fd, tmp = tempfile.mkstemp(prefix='.codex2api-install-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as out:
            out.write(data); out.flush(); os.fsync(out.fileno())
        os.chmod(tmp, mode)
        os.replace(tmp, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        if os.path.exists(tmp): os.unlink(tmp)


def archive_config_digest(path, expected):
    """Prove the pinned OCI index/manifest → Linux/amd64 config relationship."""
    with tarfile.open(path) as tar:
        def blob(digest):
            if not release.re.fullmatch('sha256:[0-9a-f]{64}', digest):
                raise ValueError('invalid archive digest')
            try:
                data = tar.extractfile('blobs/sha256/' + digest.split(':')[1]).read()
            except (KeyError, AttributeError):
                raise ValueError('prepared OCI digest absent from image archive') from None
            if 'sha256:' + hashlib.sha256(data).hexdigest() != digest:
                raise ValueError('prepared OCI blob digest mismatch')
            return data
        seen = set()
        def resolve(digest):
            if digest in seen:
                raise ValueError('cyclic OCI image graph')
            seen.add(digest)
            value = json.loads(blob(digest))
            if value.get('architecture') == 'amd64' and value.get('os') == 'linux':
                return [digest]
            if 'config' in value:
                cfg = value['config']['digest']
                if resolve(cfg) != [cfg]:
                    raise ValueError('unexpected platform configuration')
                # Docker validates layer contents during load; descriptor digests
                # here are authenticated by the prepared manifest/index digest.
                return [cfg]
            choices = []
            for entry in value.get('manifests', []):
                platform = entry.get('platform', {})
                if platform and (platform.get('architecture'), platform.get('os')) != ('amd64', 'linux'):
                    continue
                choices.extend(resolve(entry['digest']))
            return choices
        configs = resolve(expected)
        if len(configs) != 1:
            raise ValueError('exactly one prepared linux/amd64 image required')
        return configs[0]


@contextlib.contextmanager
def installed_pool(root):
    config = root / 'ipv6-pool.json'
    if not config.exists():
        yield
        return
    if config.is_symlink():
        raise ValueError('pool configuration symlink refused')
    validate_config(json.loads(config.read_text()))
    previous = root / 'network.previous'
    previous.mkdir(mode=0o700, exist_ok=False)
    pairs = [(root / 'ipv6_pool.py', pathlib.Path(POOL_TARGETS[0]), 0o700),
             (config, pathlib.Path(POOL_TARGETS[1]), 0o600),
             (root / 'codex2api-ipv6-pool.service', pathlib.Path(POOL_TARGETS[2]), 0o644)]
    snapshots = {}
    # Fully validate and snapshot before the first replacement.
    for source, target, mode in pairs:
        if source.is_symlink() or not source.is_file() or target.is_symlink():
            raise ValueError('invalid network installation source/destination')
        snapshots[target] = (target.read_bytes(), target.stat().st_mode & 0o777) if target.exists() else None
        if target.exists(): shutil.copy2(target, previous / target.name)
    unit = 'codex2api-ipv6-pool.service'
    enabled = subprocess.run(['systemctl', 'is-enabled', unit], capture_output=True, text=True).stdout.strip() == 'enabled'
    active = subprocess.run(['systemctl', 'is-active', unit], capture_output=True, text=True).stdout.strip() == 'active'
    release.private_write(previous / 'service-state.json', json.dumps({'enabled': enabled, 'active': active}))
    try:
        for source, target, mode in pairs:
            atomic_install(target, source.read_bytes(), mode)
        subprocess.run(['systemctl', 'daemon-reload'], check=True)
        subprocess.run(['systemctl', 'enable', unit], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(['systemctl', 'restart', unit], check=True)
        subprocess.run(['/usr/bin/python3', str(root / 'ipv6_pool.py'), 'ensure', '--config', str(config)], check=True)
        yield
    except BaseException:
        subprocess.run(['systemctl', 'stop', unit], check=True)
        subprocess.run(['systemctl', 'disable', unit], check=True, stdout=subprocess.DEVNULL)
        for target, snapshot in snapshots.items():
            if snapshot is None:
                target.unlink(missing_ok=True)
            else:
                atomic_install(target, snapshot[0], snapshot[1])
        subprocess.run(['systemctl', 'daemon-reload'], check=True)
        if enabled:
            subprocess.run(['systemctl', 'enable', unit], check=True, stdout=subprocess.DEVNULL)
        if active:
            subprocess.run(['systemctl', 'start', unit], check=True)
        raise


def install_pool(root):
    # Retained as a small operator API; deployment uses the context for rollback.
    with installed_pool(root):
        pass


def deploy(root, backup, release_id):
    baseline = json.loads((root / 'baseline.json').read_text())
    path = root / 'deployment-manifest.json'
    manifest = json.loads(path.read_text())
    verify_tools(root, manifest)
    args = argparse.Namespace(manifest=str(path), backup=str(backup), release_id=release_id,
        expected_container_id=baseline['container_id'], expected_image_id=baseline['image_id'],
        expected_binary_sha256=baseline['binary_sha256'], expected_database_name='codex2api',
        expected_database_user='codex2api')
    with release.release_lock():
        live = json.loads(subprocess.check_output(['docker', 'inspect', release.APP]))[0]
        binary = subprocess.check_output(['docker', 'exec', release.APP, 'sha256sum', '/proc/1/exe'], text=True).split()[0]
        release.verify_identity(live, binary, {k: baseline[k] for k in ['container_id', 'image_id', 'binary_sha256']})
        if json.loads(pathlib.Path(release.SOURCE_RELEASE).read_text()) != baseline['source']:
            raise RuntimeError('source baseline changed; stale prepared release refused')
        expected_config = archive_config_digest(root / 'image.tar', manifest['digest'])
        subprocess.run(['docker', 'load', '-i', str(root / 'image.tar')], check=True, stdout=subprocess.DEVNULL)
        actual = json.loads(subprocess.check_output(['docker', 'image', 'inspect', manifest['image']]))[0]
        # A modern builder exports an OCI index while classic Docker exposes the
        # image config digest. Keep both and verify labels/binary through the
        # established maintenance controller before any business is stopped.
        if actual['Id'] != expected_config:
            raise ValueError('loaded image config does not match prepared OCI graph')
        if actual['Id'] != manifest['digest']:
            manifest['build_index_digest'] = manifest['digest']
            manifest['digest'] = actual['Id']
        release.verify_image(actual, manifest)
        release.private_write(path, json.dumps(manifest, indent=2))
        tool = release.MaintenanceRelease(args)
        with installed_pool(root):
            tool.deploy()
        result = {'release_id': release_id, 'source': json.loads(pathlib.Path(release.SOURCE_RELEASE).read_text()),
                  'health': tool.json_request('/health', True), 'events': tool.events,
                  'rollback_image': tool.rollback_image,
                  'worker_online': tool.json_request('/api/admin/credential-ops').get('worker', {}).get('online')}
        release.private_write(backup / 'final-verification.json', json.dumps(result, indent=2))
        print(json.dumps(result), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--backup', required=True)
    parser.add_argument('--release-id', required=True)
    args = parser.parse_args()
    if not pathlib.Path(args.root).is_absolute() or not pathlib.Path(args.backup).is_absolute():
        parser.error('absolute prepared release and backup paths required')
    if not release.re.fullmatch('[a-zA-Z0-9._-]{1,80}', args.release_id):
        parser.error('invalid release id')
    os.umask(0o077)
    deploy(pathlib.Path(args.root), pathlib.Path(args.backup), args.release_id)


if __name__ == '__main__':
    main()
