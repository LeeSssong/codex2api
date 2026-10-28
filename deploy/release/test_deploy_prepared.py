import hashlib
import json
import pathlib
import tempfile
import unittest
from unittest import mock
import deploy_prepared as deploy


class PreparedTests(unittest.TestCase):
    def test_tool_hash_and_filename_guards(self):
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);(root/'tool.py').write_text('source')
            manifest={'release_tools_sha256':{'tool.py':hashlib.sha256(b'source').hexdigest()}}
            deploy.verify_tools(root,manifest)
            (root/'tool.py').write_text('changed')
            with self.assertRaises(ValueError):deploy.verify_tools(root,manifest)
            with self.assertRaises(ValueError):deploy.verify_tools(root,{'release_tools_sha256':{'../outside':'x'}})

    def test_absent_pool_does_not_mutate_any_service(self):
        with tempfile.TemporaryDirectory() as d, mock.patch.object(deploy.subprocess,'run') as run:
            deploy.install_pool(pathlib.Path(d));run.assert_not_called()

    def test_invalid_pool_rejected_before_installing(self):
        with tempfile.TemporaryDirectory() as d, mock.patch.object(deploy.subprocess,'run') as run:
            root=pathlib.Path(d);(root/'ipv6-pool.json').write_text(json.dumps({'prefix':'fd00::/112'}))
            with self.assertRaises(ValueError):deploy.install_pool(root)
            self.assertFalse((root/'network.previous').exists());run.assert_not_called()

class InstallationTests(unittest.TestCase):
    def test_atomic_install_creates_missing_and_replaces(self):
        with tempfile.TemporaryDirectory() as d:
            path=pathlib.Path(d)/'new'
            deploy.atomic_install(path,b'new',0o600)
            self.assertEqual(path.read_bytes(),b'new')
            self.assertEqual(path.stat().st_mode&0o777,0o600)
            deploy.atomic_install(path,b'replacement',0o700)
            self.assertEqual(path.read_bytes(),b'replacement')
            self.assertEqual(path.stat().st_mode&0o777,0o700)

    def pool_fixture(self, directory):
        root = pathlib.Path(directory) / 'release'
        root.mkdir()
        config = {'prefix': '2a0a:4cc0:101:799:c2a:28::/112', 'count': 128,
                  'parent': 'eth0', 'interface': 'c2a-v6', 'gateway': 'fe80::1'}
        (root / 'ipv6-pool.json').write_text(json.dumps(config))
        (root / 'ipv6_pool.py').write_text('new helper')
        (root / 'codex2api-ipv6-pool.service').write_text('new unit')
        target = pathlib.Path(directory) / 'installed'
        target.mkdir()
        targets = [target / name for name in ['ipv6_pool.py', 'ipv6-pool.json', 'codex2api-ipv6-pool.service']]
        return root, targets

    def test_first_install_with_all_three_targets_absent(self):
        with tempfile.TemporaryDirectory() as d:
            root, targets = self.pool_fixture(d)
            with mock.patch.object(deploy, 'POOL_TARGETS', targets), mock.patch.object(deploy.subprocess, 'run') as run:
                run.return_value.stdout = 'inactive'
                deploy.install_pool(root)
            self.assertEqual([p.read_bytes() for p in targets], [b'new helper', (root / 'ipv6-pool.json').read_bytes(), b'new unit'])
            self.assertEqual([p.stat().st_mode & 0o777 for p in targets], [0o700, 0o600, 0o644])
            self.assertTrue(any(call.args[0][:2] == ['systemctl', 'restart'] for call in run.call_args_list))

    def test_failed_preflight_removes_first_install_and_disables_unit(self):
        with tempfile.TemporaryDirectory() as d:
            root, targets = self.pool_fixture(d)
            def command(args, **kwargs):
                if args[0] == '/usr/bin/python3':
                    raise RuntimeError('preflight failed')
                return mock.Mock(stdout='inactive')
            with mock.patch.object(deploy, 'POOL_TARGETS', targets), mock.patch.object(deploy.subprocess, 'run', side_effect=command) as run:
                with self.assertRaisesRegex(RuntimeError, 'preflight failed'):
                    deploy.install_pool(root)
            self.assertFalse(any(p.exists() for p in targets))
            self.assertIn(mock.call(['systemctl', 'stop', 'codex2api-ipv6-pool.service'], check=True), run.call_args_list)
            self.assertFalse(any(c.args[0][:2] == ['systemctl', 'start'] for c in run.call_args_list))

    def test_failed_deploy_restores_previous_files_modes_and_service(self):
        with tempfile.TemporaryDirectory() as d:
            root, targets = self.pool_fixture(d)
            for p in targets:
                p.write_bytes(b'previous ' + p.name.encode())
                p.chmod(0o640)
            before = [p.read_bytes() for p in targets]
            def command(args, **kwargs):
                return mock.Mock(stdout='enabled' if args[1] == 'is-enabled' else 'active')
            with mock.patch.object(deploy, 'POOL_TARGETS', targets), mock.patch.object(deploy.subprocess, 'run', side_effect=command) as run:
                with self.assertRaisesRegex(RuntimeError, 'deployment failed'):
                    with deploy.installed_pool(root):
                        self.assertEqual(targets[0].read_bytes(), b'new helper')
                        raise RuntimeError('deployment failed')
            self.assertEqual([p.read_bytes() for p in targets], before)
            self.assertEqual([p.stat().st_mode & 0o777 for p in targets], [0o640] * 3)
            self.assertIn(mock.call(['systemctl', 'start', 'codex2api-ipv6-pool.service'], check=True), run.call_args_list)

    def test_partial_file_install_failure_restores_prior_state(self):
        with tempfile.TemporaryDirectory() as d:
            root, targets = self.pool_fixture(d)
            targets[0].write_bytes(b'previous helper')
            real_install = deploy.atomic_install
            count = 0
            def install(path, data, mode):
                nonlocal count
                count += 1
                if count == 2:
                    raise OSError('disk failure')
                return real_install(path, data, mode)
            with mock.patch.object(deploy, 'POOL_TARGETS', targets), mock.patch.object(deploy.subprocess, 'run') as run, mock.patch.object(deploy, 'atomic_install', side_effect=install):
                run.return_value.stdout = 'inactive'
                with self.assertRaisesRegex(OSError, 'disk failure'):
                    deploy.install_pool(root)
            self.assertEqual(targets[0].read_bytes(), b'previous helper')
            self.assertFalse(targets[1].exists())
            self.assertFalse(targets[2].exists())

    def test_first_install_stop_unit_not_found_still_restores_and_preserves_error(self):
        import subprocess
        with tempfile.TemporaryDirectory() as d:
            root, targets = self.pool_fixture(d)
            real_install = deploy.atomic_install
            count = 0
            def install(path, data, mode):
                nonlocal count
                count += 1
                if count == 2:
                    raise OSError('original install failure')
                return real_install(path, data, mode)
            def command(args, **kwargs):
                if args[:2] in (['systemctl', 'stop'], ['systemctl', 'disable']):
                    if kwargs.get('check'):
                        raise subprocess.CalledProcessError(5, args)
                    return mock.Mock(stdout='', returncode=5)
                return mock.Mock(stdout='inactive', returncode=0)
            with mock.patch.object(deploy, 'POOL_TARGETS', targets), mock.patch.object(deploy.subprocess, 'run', side_effect=command), mock.patch.object(deploy, 'atomic_install', side_effect=install):
                with self.assertRaisesRegex(OSError, 'original install failure'):
                    deploy.install_pool(root)
            self.assertFalse(any(p.exists() for p in targets))

    def test_oci_graph_proves_index_to_config_and_rejects_wrong_or_tampered(self):
        import io,tarfile
        with tempfile.TemporaryDirectory() as d:
            path=pathlib.Path(d)/'image.tar'
            cfg=json.dumps({'architecture':'amd64','os':'linux'}).encode()
            cd='sha256:'+hashlib.sha256(cfg).hexdigest()
            manifest=json.dumps({'config':{'digest':cd},'layers':[]}).encode();md='sha256:'+hashlib.sha256(manifest).hexdigest()
            index=json.dumps({'manifests':[{'digest':md,'platform':{'architecture':'amd64','os':'linux'}}]}).encode();idx='sha256:'+hashlib.sha256(index).hexdigest()
            def write(tamper=False):
                with tarfile.open(path,'w') as tar:
                    for digest,data in [(idx,index),(md,manifest),(cd,b'corrupt' if tamper else cfg)]:
                        info=tarfile.TarInfo('blobs/sha256/'+digest.split(':')[1]);info.size=len(data);tar.addfile(info,io.BytesIO(data))
            write();self.assertEqual(deploy.archive_config_digest(path,idx),cd)
            with self.assertRaises(ValueError):deploy.archive_config_digest(path,'sha256:'+'0'*64)
            write(True)
            with self.assertRaises(ValueError):deploy.archive_config_digest(path,idx)
