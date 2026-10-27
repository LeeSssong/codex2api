import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest
import types
import os
import subprocess

MODULE = pathlib.Path(__file__).with_name('maintenance_release.py')
spec = importlib.util.spec_from_file_location('maintenance_release', MODULE)
release = importlib.util.module_from_spec(spec)
if MODULE.exists():
    spec.loader.exec_module(release)


class GuardTests(unittest.TestCase):
    def test_api_key_configuration_excludes_live_usage_counters(self):
        self.assertEqual(release.api_key_config_fields(['id', 'key', 'enabled', 'limits', 'quota_limit', 'quota_used', 'total_used', 'last_reset_at', 'reset_count']), ['id', 'key', 'enabled', 'limits', 'quota_limit'])
    def test_container_config_fingerprint_ignores_mount_enumeration_order(self):
        c = {'Config': {'Image': 'old'}, 'HostConfig': {}, 'Mounts': [{'Destination': '/a', 'RW': True}, {'Destination': '/b', 'RW': False}]}
        other = copy.deepcopy(c); other['Mounts'].reverse()
        self.assertEqual(release.container_config_fingerprint(c), release.container_config_fingerprint(other))
        other['Mounts'][0]['RW'] = True
        self.assertNotEqual(release.container_config_fingerprint(c), release.container_config_fingerprint(other))

    def test_verified_codex_legacy_alias_is_same_route(self):
        c = {'apps': {'http': {'servers': {'https': {'routes': [
            {'match': [{'host': ['codex.xingqiaolab.top', '64-83-10-67.nip.io']}], 'handle': []}]}}}}}
        self.assertIs(release.codex_route(c), c['apps']['http']['servers']['https']['routes'][0])

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

    def test_candidate_writer_boundary_prohibits_snapshot_restore(self):
        for opened, writer_started in [(False, False), (False, True), (True, True)]:
            with tempfile.TemporaryDirectory() as tmp:
                tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
                tool.opened = opened; tool.migrated = True
                tool.writer_started = writer_started
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
                tool.restore_source_record = lambda: None
                if writer_started:
                    with self.assertRaisesRegex(RuntimeError, 'candidate writers'):
                        tool.rollback()
                else:
                    tool.rollback()
                restored = any('pg_restore --exit-on-error' in ' '.join(call) for call in calls)
                self.assertEqual(restored, not writer_started and not opened)
                self.assertFalse(writer_started and any('dropdb' in ' '.join(call) for call in calls))

    def test_rehearsal_environment_uses_memory_cache_and_scratch_database(self):
        original = {'DATABASE_DRIVER': 'postgres', 'DATABASE_HOST': 'postgres', 'DATABASE_USER': 'codex',
                    'DATABASE_NAME': 'production', 'DATABASE_PASSWORD': 'test-only',
                    'CACHE_DRIVER': 'redis', 'REDIS_ADDR': 'production:6379',
                    'CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR': '/production-runtime'}
        values = release.rehearsal_environment(original, 'scratch')
        self.assertEqual(values['CACHE_DRIVER'], 'memory')
        self.assertEqual(values['DATABASE_NAME'], 'scratch')
        self.assertNotIn('REDIS_ADDR', values)
        self.assertNotIn('CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR', values)
        self.assertEqual(original['DATABASE_NAME'], 'production')
        with tempfile.TemporaryDirectory() as tmp:
            source = pathlib.Path(tmp) / 'check.go'
            source.write_text('package main\nimport ("fmt"; "os"; "github.com/codex2api/config")\nfunc main(){ c,e:=config.Load("/does-not-exist"); if e!=nil {fmt.Println(e); os.Exit(1)}; if c.Cache.Driver!="memory" || c.Database.DBName!="scratch" {os.Exit(2)} }\n')
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith(('DATABASE_', 'CACHE_', 'REDIS_', 'CODEX_'))}
            environment.update(values)
            result = subprocess.run(['go', 'run', str(source)], cwd=MODULE.parents[2],
                                    env=environment, capture_output=True, text=True, timeout=90)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_failed_full_app_start_is_already_a_writer_boundary(self):
        with tempfile.TemporaryDirectory() as tmp:
            tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
            tool.directory = pathlib.Path(tmp); tool.compose = tool.directory / 'compose.json'; tool.compose.write_text('{}')
            tool.config = {'services': {'codex2api': {'image': 'old'}}}; tool.manifest = {'digest': 'new'}
            tool.migration_name = 'migration'; tool.stopped = tool.migrated = tool.opened = tool.writer_started = False
            for method in ['preflight', 'prepare_rollback', 'rehearse', 'check_identity', 'check_source_unchanged', 'verify_protected', 'ready']:
                setattr(tool, method, lambda: None)
            tool.network_gate = lambda enabled: None
            tool.caddy = lambda: {'apps': {'http': {'servers': {'s': {'routes': [
                {'match': [{'host': ['codex.xingqiaolab.top']}], 'handle': []}]}}}}}
            tool.load_caddy = lambda config: None; tool.event = lambda name: None
            tool.drain = lambda: 60; tool.dump = lambda path: None
            tool.retained = lambda: {}; tool.verify_retained = lambda before: None
            observed = []
            def dc(*arguments, **kwargs):
                if arguments[0] == 'up':
                    observed.append(tool.writer_started)
                    raise RuntimeError('app start timed out')
            tool.dc = dc
            tool.rollback = lambda: observed.append(tool.writer_started)
            with self.assertRaisesRegex(RuntimeError, 'app start timed out'):
                tool.deploy()
            self.assertEqual(observed, [True, True])

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

    def source_fixture(self):
        current = {'revision': 'a' * 40, 'tree': 'b' * 40, 'digest': 'sha256:' + 'c' * 64,
                   'binary_sha256': 'd' * 64, 'upstream_revision': 'e' * 40}
        candidate = {'revision': 'f' * 40, 'tree': '1' * 40, 'digest': 'sha256:' + '2' * 64,
                     'binary_sha256': '3' * 64, 'upstream_revision': '4' * 40,
                     'baseline_revision': current['revision'], 'baseline_binary_sha256': current['binary_sha256'],
                     'ancestry_verified': True}
        return current, candidate

    def test_stale_source_baseline_rejected_even_with_current_live_identity(self):
        current, candidate = self.source_fixture()
        release.verify_source_baseline(current, candidate, current['binary_sha256'])
        for field, value in [('baseline_revision', '9' * 40), ('ancestry_verified', False),
                             ('baseline_binary_sha256', '8' * 64)]:
            stale = copy.deepcopy(candidate); stale[field] = value
            with self.assertRaises(ValueError):
                release.verify_source_baseline(current, stale, current['binary_sha256'])
        malformed = copy.deepcopy(current); malformed['tree'] = 'unknown'
        with self.assertRaises(ValueError):
            release.verify_source_baseline(malformed, candidate, current['binary_sha256'])
        with self.assertRaises(ValueError):
            release.verify_source_baseline(current, candidate, '0' * 64)

    def test_first_legacy_baseline_requires_explicit_binary_and_preservation_note(self):
        _, candidate = self.source_fixture()
        candidate.update(baseline_revision='legacy-unversioned', ancestry_verified=False,
                         legacy_preservation_note='Preserve shared patch snapshot and exact live rollback binary.')
        release.verify_source_baseline(None, candidate, 'd' * 64)
        for field, value in [('baseline_revision', 'a' * 40), ('ancestry_verified', True),
                             ('legacy_preservation_note', ''), ('baseline_binary_sha256', '0' * 64)]:
            bad = copy.deepcopy(candidate); bad[field] = value
            with self.assertRaises(ValueError):
                release.verify_source_baseline(None, bad, 'd' * 64)
        current, _ = self.source_fixture()
        with self.assertRaises(ValueError):
            release.verify_source_baseline(current, candidate, 'd' * 64)

    def test_source_record_is_written_atomically_and_rollback_restores_prior_bytes(self):
        current, candidate = self.source_fixture()
        with tempfile.TemporaryDirectory() as tmp:
            tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
            tool.source_path = pathlib.Path(tmp) / 'source-release.json'
            tool.source_before = json.dumps(current, indent=4).encode()
            tool.source_path.write_bytes(tool.source_before)
            tool.manifest = candidate; tool.args = types.SimpleNamespace(release_id='test')
            tool.publish_source_record()
            recorded = json.loads(tool.source_path.read_bytes())
            for field in ['revision', 'tree', 'digest', 'binary_sha256', 'upstream_revision']:
                self.assertEqual(recorded[field], candidate[field])
            self.assertEqual(recorded['source_manifest_path'], '/opt/codex2api/source-release.json')
            self.assertEqual(tool.source_path.stat().st_mode & 0o777, 0o600)
            tool.restore_source_record()
            self.assertEqual(tool.source_path.read_bytes(), tool.source_before)
            tool.source_before = None; tool.source_path.unlink()
            tool.publish_source_record(); tool.restore_source_record()
            self.assertFalse(tool.source_path.exists())

    def test_changed_source_record_before_publish_is_never_overwritten(self):
        current, candidate = self.source_fixture()
        with tempfile.TemporaryDirectory() as tmp:
            tool = release.MaintenanceRelease.__new__(release.MaintenanceRelease)
            tool.source_path = pathlib.Path(tmp) / 'source-release.json'
            tool.source_before = json.dumps(current).encode()
            tool.source_path.write_text('newer release')
            tool.manifest = candidate; tool.args = types.SimpleNamespace(release_id='test')
            with self.assertRaises(RuntimeError):
                tool.publish_source_record()
            self.assertEqual(tool.source_path.read_text(), 'newer release')
            with self.assertRaises(RuntimeError):
                tool.restore_source_record()
            self.assertEqual(tool.source_path.read_text(), 'newer release')


if __name__ == '__main__':
    unittest.main()
