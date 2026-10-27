import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest
import types

MODULE = pathlib.Path(__file__).with_name('maintenance_release.py')
spec = importlib.util.spec_from_file_location('maintenance_release', MODULE)
release = importlib.util.module_from_spec(spec)
if MODULE.exists():
    spec.loader.exec_module(release)


class GuardTests(unittest.TestCase):
    def container(self):
        return {'Id': 'container-id', 'Image': 'sha256:old', 'State': {'Running': True},
                'Config': {'Env': ['CODEX_PORT=18080'], 'Labels': {
                    'com.docker.compose.project.config_files': '/opt/codex2api/compose.netcup.json',
                    'com.docker.compose.project': 'codex2api',
                    'com.docker.compose.project.working_dir': '/opt/codex2api'}},
                'NetworkSettings': {'Ports': {'18080/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '18087'}]}}}

    def test_mismatched_identity_aborts_before_prepare(self):
        for field in ['container_id', 'image_id', 'binary_sha256']:
            expected = {'container_id': 'container-id', 'image_id': 'sha256:old', 'binary_sha256': 'a' * 64}
            expected[field] = 'wrong'
            with self.assertRaisesRegex(ValueError, 'identity'):
                release.verify_identity(self.container(), 'a' * 64, expected)

    def test_port_uses_host_binding_not_internal_port(self):
        self.assertEqual(release.discover_port(self.container()), (18080, 18087))

    def test_active_compose_comes_from_container_label(self):
        self.assertEqual(release.discover_compose(self.container()),
                         ('codex2api', '/opt/codex2api', '/opt/codex2api/compose.netcup.json'))
        c = self.container(); c['Config']['Labels']['com.docker.compose.project.config_files'] += ',/other.yml'
        with self.assertRaises(ValueError):
            release.discover_compose(c)

    def test_compose_preserves_runtime_egress_restart_and_other_services(self):
        original = {'services': {'codex2api': {'image': 'old', 'restart': 'no',
                    'environment': {'CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR': '/opt/credential-runtime',
                                    'FIXED_EGRESS': 'fixed'},
                    'volumes': [{'source': '/runtime-newyork', 'target': '/opt/credential-runtime', 'read_only': True}],
                    'ports': [{'target': 18080, 'published': '18087'}]},
                    'postgres': {'image': 'postgres:18'}}, 'networks': {'codex2api-net': {'external': True}}}
        snapshot = copy.deepcopy(original)
        result = release.with_image(original, 'new')
        result['services']['codex2api']['image'] = 'old'
        self.assertEqual(result, snapshot)
        self.assertEqual(original, snapshot)

    def test_lock_contention_fails_without_waiting(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / 'release.lock'
            with release.release_lock(path):
                with self.assertRaises(BlockingIOError):
                    with release.release_lock(path):
                        self.fail('second lock acquired')

    def test_fingerprint_is_order_stable_and_detects_enabled_change(self):
        rows = [{'account_id': 2, 'enabled': False}, {'account_id': 1, 'enabled': True}]
        self.assertEqual(release.fingerprint(rows), release.fingerprint(list(reversed(rows))))
        changed = copy.deepcopy(rows); changed[1]['enabled'] = False
        self.assertNotEqual(release.fingerprint(rows), release.fingerprint(changed))

    def test_caddy_changes_only_codex_handle(self):
        c = {'apps': {'http': {'servers': {'https': {'routes': [
            {'match': [{'host': ['codex.xingqiaolab.top']}], 'handle': [{'handler': 'reverse_proxy'}]},
            {'match': [{'host': ['api.xingqiaolab.top']}], 'handle': [{'handler': 'reverse_proxy'}]}]}}}}}
        updated = release.maintenance_config(c)
        self.assertEqual(c['apps']['http']['servers']['https']['routes'][1],
                         updated['apps']['http']['servers']['https']['routes'][1])
        self.assertEqual(release.codex_route(updated)['handle'][0]['status_code'], 503)
        self.assertEqual(release.codex_route(c)['handle'][0]['handler'], 'reverse_proxy')

    def test_new_source_provenance_must_match_every_label(self):
        m = {'revision': 'a' * 40, 'tree': 'b' * 40, 'upstream_revision': 'c' * 40,
             'binary_sha256': 'd' * 64, 'digest': 'sha256:new', 'image': 'new', 'build_version': 'release-test'}
        labels = {'org.opencontainers.image.revision': m['revision'], 'io.xingqiao.source-tree': m['tree'],
                  'io.xingqiao.upstream-revision': m['upstream_revision']}
        image = {'Id': m['digest'], 'Architecture': 'amd64', 'Config': {'Labels': labels}}
        release.verify_image(image, m)
        for key in labels:
            bad = copy.deepcopy(image); bad['Config']['Labels'][key] = 'wrong'
            with self.assertRaises(ValueError):
                release.verify_image(bad, m)

    def test_preflight_mismatch_performs_no_mutating_command(self):
        tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
        tool.args = types.SimpleNamespace(expected_container_id='wrong', expected_image_id='sha256:old', expected_binary_sha256='a' * 64)
        calls = []
        def run(command, **kwargs):
            calls.append(command)
            if command[:2] == ['docker', 'inspect']:
                return json.dumps([self.container()]).encode()
            if command[:3] == ['docker', 'exec', 'codex2api']:
                return ('a' * 64 + ' /proc/1/exe').encode()
            self.fail('mutation before identity check: ' + repr(command))
        tool.run = run
        with self.assertRaisesRegex(ValueError, 'identity'):
            tool.preflight()
        self.assertEqual(len(calls), 2)

    def test_post_open_rollback_never_restores_old_database(self):
        for opened in [True, False]:
            with tempfile.TemporaryDirectory() as tmp:
                tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
                tool.opened = opened; tool.migrated = True
                tool.directory = pathlib.Path(tmp); (tool.directory / 'stopped.dump').write_bytes(b'dump')
                tool.compose = tool.directory / 'compose.json'; tool.compose.write_text('{}')
                tool.config = {'services': {'codex2api': {'image': 'old'}}}; tool.rollback_image = 'rollback'
                tool.migration_name = 'migration'; tool.args = types.SimpleNamespace(expected_binary_sha256='oldhash')
                calls = []
                tool.event = lambda name: None
                tool.network_gate = lambda enable: None
                tool.caddy = lambda: {'apps': {'http': {'servers': {'s': {'routes': [
                    {'match': [{'host': ['codex.xingqiaolab.top']}], 'handle': []}]}}}}}
                tool.load_caddy = lambda config: None
                tool.run = lambda command, *args, **kwargs: calls.append(command) or b''
                tool.dc = lambda *args, **kwargs: None
                tool.ready = lambda: None
                tool.drain = lambda: 60
                tool.binary_hash = lambda: 'oldhash'
                tool.verify_protected = lambda: None
                tool.restore_route = lambda: None
                tool.rollback()
                restored = any('pg_restore --exit-on-error' in ' '.join(call) for call in calls)
                self.assertEqual(restored, not opened)

    def test_stable_projection_ignores_only_new_columns_and_volatile_columns(self):
        before = [{'id': 1, 'enabled': True, 'updated_at': 'old'}]
        after = [{'id': 1, 'enabled': True, 'updated_at': 'new', 'new_upstream_option': False}]
        fields = ['id', 'enabled']
        self.assertEqual(release.project_rows(before, fields), release.project_rows(after, fields))
        after[0]['enabled'] = False
        self.assertNotEqual(release.project_rows(before, fields), release.project_rows(after, fields))

    def test_history_and_configuration_fingerprint_rejects_loss(self):
        tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
        before = {'intelligence_settings': 'same', 'history_floor': [7, 8]}
        tool.retained = lambda database, **kwargs: {'intelligence_settings': 'same', 'history_floor': [6, 8]}
        with self.assertRaisesRegex(RuntimeError, 'history'):
            tool.verify_retained(before)
        tool.retained = lambda database, **kwargs: {'intelligence_settings': 'changed', 'history_floor': [8, 9]}
        with self.assertRaisesRegex(RuntimeError, 'configuration'):
            tool.verify_retained(before)

    def test_quota_smoke_requires_requested_rows_and_both_windows(self):
        valid = {'stats': {'12': {'quota_5h': {'state': 'no_usage'}, 'quota_7d': {'state': 'low_sample', 'used_usd': 2.0}}}}
        release.validate_quota_stats(valid, [12])
        for bad in [{'stats': {}}, {'stats': {'12': {'quota_5h': {'state': 'ready'}}}},
                    {'stats': {'12': {'quota_5h': {'state': 'ready', 'used_usd': float('nan')}, 'quota_7d': {'state': 'ready'}}}}]:
            with self.assertRaises(RuntimeError):
                release.validate_quota_stats(bad, [12])

    def test_live_service_must_match_ports_and_runtime_mount(self):
        c = self.container(); c['HostConfig'] = {'RestartPolicy': {'Name': 'no'}}
        c['Mounts'] = [{'Destination': '/runtime', 'Source': '/srv/runtime', 'RW': False}]
        service = {'environment': {'CODEX_PORT': '18080'}, 'restart': 'no',
                   'ports': [{'target': 18080, 'published': '18087'}],
                   'volumes': [{'type': 'bind', 'source': '/srv/runtime', 'target': '/runtime', 'read_only': True}]}
        release.verify_service(c, service)
        for field in ['ports', 'volumes']:
            bad = copy.deepcopy(service)
            if field == 'ports': bad[field][0]['published'] = '18080'
            else: bad[field][0]['source'] = '/other'
            with self.assertRaises(ValueError):
                release.verify_service(c, bad)

    def test_binary_manifest_path_and_digest_are_verified(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / 'codex2api'; path.write_bytes(b'executable')
            manifest = {'binary_path': str(path), 'binary_sha256': release.hashlib.sha256(b'executable').hexdigest()}
            release.verify_binary_file(manifest)
            path.write_bytes(b'changed')
            with self.assertRaises(ValueError):
                release.verify_binary_file(manifest)

    def test_service_check_rejects_missing_credential_environment(self):
        c = self.container(); c['Config']['Env'].append('CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR=/runtime')
        c['HostConfig'] = {'RestartPolicy': {'Name': 'no'}}; c['Mounts'] = []
        service = {'environment': {'CODEX_PORT': '18080'}, 'ports': [{'target': 18080, 'published': '18087'}]}
        with self.assertRaises(ValueError):
            release.verify_service(c, service)


if __name__ == '__main__':
    unittest.main()
