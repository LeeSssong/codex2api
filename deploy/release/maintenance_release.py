#!/usr/bin/env python3
"""Explicit single-writer maintenance deployment; never runs automatically.

All resolved config and subprocess errors stay in the owner-only backup.
rehearse uses migrate-only against a restored disposable database, no workers.
"""
import argparse
import contextlib
import datetime
import copy
import fcntl
import hashlib
import ipaddress
import json
import math
import os
import pathlib
import re
import subprocess
import tempfile
import time
import urllib.request

APP = 'codex2api'
PG = 'codex2api-postgres'
CADDY = 'sub2api-caddy-1'
HOST = 'codex.xingqiaolab.top'
EXE = '/usr/local/bin/codex2api'
SOURCE_RELEASE = '/opt/codex2api/source-release.json'


def fingerprint(rows):
    values = sorted(json.dumps(row, sort_keys=True, separators=(',', ':')) for row in rows)
    return hashlib.sha256(chr(10).join(values).encode()).hexdigest()


def project_rows(rows, fields):
    return fingerprint([{key: row.get(key) for key in fields} for row in rows])


def api_key_config_fields(fields):
    return [field for field in fields if field not in {'quota_used', 'total_used', 'last_used_at', 'updated_at', 'last_reset_at', 'reset_count'}]


def container_config_fingerprint(container):
    return fingerprint([container['Config'], container['HostConfig'], sorted(container['Mounts'], key=lambda item: item['Destination'])])


def rehearsal_environment(environment, database):
    values = {key: value for key, value in environment.items() if key.startswith('DATABASE_')}
    values['DATABASE_NAME'] = database
    # config.Load runs before migrate-only; use a valid isolated cache config.
    values['CACHE_DRIVER'] = 'memory'
    if any(chr(10) in value or chr(13) in value for value in values.values()):
        raise ValueError('multiline database environment not supported')
    return values


def verify_identity(container, binary_sha256, expected):
    actual = dict(container_id=container['Id'], image_id=container['Image'], binary_sha256=binary_sha256)
    if actual != expected or not container['State']['Running']:
        raise ValueError('production identity changed')


def discover_port(container):
    env = dict(item.split('=', 1) for item in container['Config']['Env'])
    internal = int(env.get('CODEX_PORT', '18080'))
    ports = {int(item['HostPort']) for item in container['NetworkSettings']['Ports'].get(str(internal) + '/tcp') or []}
    if len(ports) != 1:
        raise ValueError('one published app port required')
    return internal, ports.pop()


def discover_compose(container):
    labels = container['Config']['Labels']
    path = labels.get('com.docker.compose.project.config_files', '')
    project = labels.get('com.docker.compose.project', '')
    root = labels.get('com.docker.compose.project.working_dir', '')
    if not project or not pathlib.Path(root).is_absolute() or not pathlib.Path(path).is_absolute() or ',' in path:
        raise ValueError('one active Compose file and explicit project/root required')
    return project, root, path


def with_image(config, image):
    result = copy.deepcopy(config)
    result['services'][APP]['image'] = image
    return result


def verify_service(container, service):
    env = dict(x.split('=', 1) for x in container['Config']['Env'])
    for key in env:
        if key.startswith(('CODEX', 'DATABASE_', 'CACHE_', 'REDIS_', 'FIXED_', 'MIHOMO_', 'RESIN_')) or key in ['ADMIN_SECRET', 'TZ']:
            if key not in service.get('environment', {}):
                raise ValueError('running app environment key missing from Compose')
    for key, value in service.get('environment', {}).items():
        normalized = '' if value is None else str(value)
        if env.get(key, '') != normalized:
            raise ValueError('active Compose environment differs from running app')
    if service.get('restart', 'no') != container['HostConfig']['RestartPolicy']['Name']:
        raise ValueError('active Compose restart policy differs from running app')
    internal, host = discover_port(container)
    published = {(int(p['target']), int(p['published'])) for p in service.get('ports', []) if p.get('protocol', 'tcp') == 'tcp'}
    if published != {(internal, host)}:
        raise ValueError('active Compose ports differ from running app')
    mounts = {x['Destination']: x for x in container['Mounts']}
    intended = {x['target']: x for x in service.get('volumes', [])}
    if set(mounts) != set(intended):
        raise ValueError('active Compose mount destinations differ from running app')
    for target, mount in intended.items():
        actual = mounts[target]
        if mount.get('type') != 'bind' or pathlib.Path(mount['source']).resolve() != pathlib.Path(actual['Source']).resolve() or bool(mount.get('read_only', False)) == actual['RW']:
            raise ValueError('active Compose mount differs from running app')


def validate_quota_stats(payload, ids):
    stats = payload.get('stats', {})
    for account_id in ids:
        row = stats.get(str(account_id))
        if not isinstance(row, dict):
            raise RuntimeError('quota smoke returned no requested account')
        for key in ['quota_5h', 'quota_7d']:
            window = row.get(key)
            if not isinstance(window, dict) or window.get('state') not in ['ready', 'unavailable', 'stale', 'no_usage', 'low_sample', 'error']:
                raise RuntimeError('quota smoke window/state missing')
            if window['state'] == 'error':
                raise RuntimeError('quota smoke reports a calculation error')
            for value in window.values():
                if isinstance(value, (int, float)) and not isinstance(value, bool) and (not math.isfinite(value) or value < 0):
                    raise RuntimeError('quota smoke has invalid numeric value')


def verify_image(image, manifest):
    for key in ['revision', 'tree', 'upstream_revision']:
        if not re.fullmatch('[0-9a-f]{40}', manifest[key]):
            raise ValueError('invalid source provenance')
    if not re.fullmatch('[0-9a-f]{64}', manifest['binary_sha256']):
        raise ValueError('invalid binary provenance')
    labels = image['Config'].get('Labels') or {}
    expected = {'org.opencontainers.image.revision': manifest['revision'],
                'io.xingqiao.source-tree': manifest['tree'],
                'io.xingqiao.upstream-revision': manifest['upstream_revision']}
    if image['Id'] != manifest['digest'] or image['Architecture'] != 'amd64' or any(labels.get(k) != v for k, v in expected.items()):
        raise ValueError('new image provenance mismatch')


def verify_binary_file(manifest):
    path = pathlib.Path(manifest['binary_path'])
    if not path.is_absolute() or not path.is_file() or path.is_symlink():
        raise ValueError('absolute immutable binary path required')
    if hashlib.sha256(path.read_bytes()).hexdigest() != manifest['binary_sha256']:
        raise ValueError('manifest binary path digest mismatch')


def verify_source_baseline(current, candidate, live_binary_sha256):
    """Trust locally verified ancestry only against the unchanged server head.

    This host deliberately does not fetch Git or invent legacy ancestry.
    The caller supplies merge-base evidence in its reviewed build manifest.
    """
    if candidate.get('baseline_binary_sha256') != live_binary_sha256:
        raise ValueError('source baseline executable differs from expected live executable')
    if current is None:
        note = candidate.get('legacy_preservation_note')
        if candidate.get('baseline_revision') != 'legacy-unversioned' or candidate.get('ancestry_verified') is not False:
            raise ValueError('first release requires explicit legacy baseline without claimed ancestry')
        if not isinstance(note, str) or len(note.strip()) < 20:
            raise ValueError('legacy source preservation evidence required')
        return
    for field in ['revision', 'tree', 'upstream_revision']:
        if not re.fullmatch('[0-9a-f]{40}', current.get(field, '')):
            raise ValueError('invalid authoritative source release')
    if not re.fullmatch('[0-9a-f]{64}', current.get('binary_sha256', '')) or not re.fullmatch('sha256:[0-9a-f]{64}', current.get('digest', '')):
        raise ValueError('invalid authoritative artifact digest')
    if current['binary_sha256'] != live_binary_sha256:
        raise ValueError('authoritative source release no longer matches running executable')
    if candidate.get('baseline_revision') != current['revision'] or candidate.get('ancestry_verified') is not True:
        raise ValueError('stale source baseline or locally verified ancestry missing')


def atomic_source_write(path, data):
    """Durably replace a private source record, including the first release."""
    path = pathlib.Path(path)
    if path.is_symlink():
        raise ValueError('source-release symlink refused')
    fd, temporary = tempfile.mkstemp(prefix='.source-release-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(data); output.flush(); os.fsync(output.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


@contextlib.contextmanager
def release_lock(path='/var/lock/codex2api-release.lock'):
    with open(path, 'a') as handle:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def codex_route(config):
    routes = []
    for server in config.get('apps', {}).get('http', {}).get('servers', {}).values():
        for route in server.get('routes', []):
            hosts = [h for match in route.get('match', []) for h in match.get('host', [])]
            if HOST in hosts:
                if not set(hosts).issubset({HOST, '64-83-10-67.nip.io'}):
                    raise ValueError('Codex route shares another hostname')
                routes.append(route)
    if len(routes) != 1:
        raise ValueError('exactly one Codex route required')
    return routes[0]


def maintenance_config(config):
    result = copy.deepcopy(config)
    codex_route(result)['handle'] = [{'handler': 'static_response', 'status_code': 503,
        'body': 'Codex2API maintenance; retry shortly.', 'headers': {'Retry-After': ['120']}}]
    return result


def assert_codex_only(before, after):
    left, right = copy.deepcopy(before), copy.deepcopy(after)
    codex_route(left)['handle'] = []
    codex_route(right)['handle'] = []
    if left != right:
        raise RuntimeError('non-Codex ingress changed')


def private_write(path, data):
    path = pathlib.Path(path)
    if path.is_symlink():
        raise ValueError('refuse symlink artifact')
    path.write_bytes(data if isinstance(data, bytes) else data.encode())
    path.chmod(0o600)


def atomic_write(path, data):
    path = pathlib.Path(path)
    info = path.stat()
    fd, temp = tempfile.mkstemp(prefix='.maintenance-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(data); output.flush(); os.fsync(output.fileno())
        os.chmod(temp, info.st_mode & 0o777); os.chown(temp, info.st_uid, info.st_gid)
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


class MaintenanceRelease:
    def __init__(self, args):
        self.args = args
        self.manifest = json.loads(pathlib.Path(args.manifest).read_text())
        self.directory = pathlib.Path(args.backup)
        self.events = []
        self.maintenance = self.stopped = self.migrated = self.opened = False
        self.writer_started = False
        self.log = None

    def run(self, command, data=None, timeout=180, output=None):
        err = self.log.open('ab') if self.log else subprocess.DEVNULL
        try:
            result = subprocess.run(command, input=data, stdout=output or subprocess.PIPE,
                                    stderr=err, timeout=timeout, check=False)
        finally:
            if self.log:
                err.close()
        if result.returncode:
            raise RuntimeError('release command failed; inspect protected command log')
        return result.stdout

    def inspect(self, target):
        return json.loads(self.run(['docker', 'inspect', target]))[0]

    def binary_hash(self):
        return self.run(['docker', 'exec', APP, 'sha256sum', '/proc/1/exe']).decode().split()[0]

    def expected(self):
        return dict(container_id=self.args.expected_container_id, image_id=self.args.expected_image_id,
                    binary_sha256=self.args.expected_binary_sha256)

    def check_identity(self):
        current = self.inspect(APP)
        verify_identity(current, self.binary_hash(), self.expected())
        return current

    def source_bytes(self):
        if self.source_path.is_symlink():
            raise ValueError('source-release symlink refused')
        if self.source_path.exists():
            return self.source_path.read_bytes()
        return None

    def check_source_unchanged(self):
        if self.source_bytes() != self.source_before:
            raise RuntimeError('authoritative source release changed; refusing stale candidate')

    def publish_source_record(self):
        self.check_source_unchanged()
        fields = ['revision', 'tree', 'digest', 'binary_sha256', 'upstream_revision',
                  'baseline_revision', 'baseline_binary_sha256', 'ancestry_verified']
        record = {field: self.manifest[field] for field in fields}
        record['release_id'] = self.args.release_id
        record['source_manifest_path'] = SOURCE_RELEASE
        if 'legacy_preservation_note' in self.manifest:
            record['legacy_preservation_note'] = self.manifest['legacy_preservation_note']
        self.published_source_bytes = (json.dumps(record, indent=2, sort_keys=True) + chr(10)).encode()
        atomic_source_write(self.source_path, self.published_source_bytes)

    def restore_source_record(self):
        current = self.source_bytes()
        if current == self.source_before:
            return
        if current != getattr(self, 'published_source_bytes', None):
            raise RuntimeError('source release changed outside this attempt; rollback will not overwrite it')
        if self.source_before is None:
            self.source_path.unlink()
            directory = os.open(self.source_path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        else:
            atomic_source_write(self.source_path, self.source_before)

    def dc(self, *arguments, timeout=180):
        return self.run(['docker', 'compose', '-p', self.project, '--project-directory', self.root,
                        '-f', str(self.compose), *arguments], timeout=timeout)

    def sql(self, query, database=None):
        command = ['docker', 'exec', '-i']
        if database:
            command += ['-e', 'RELEASE_DATABASE=' + database]
        command += [PG, 'sh', '-c', 'db="$POSTGRES_DB"; if [ -n "$RELEASE_DATABASE" ]; then db="$RELEASE_DATABASE"; fi; exec psql -XqAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$db"']
        return self.run(command, query.encode()).decode().strip()

    def dump(self, destination):
        with pathlib.Path(destination).open('wb') as output:
            self.run(['docker', 'exec', PG, 'sh', '-c', 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc'], output=output, timeout=300)
        self.run(['docker', 'exec', '-i', PG, 'pg_restore', '--list'], pathlib.Path(destination).read_bytes())
        pathlib.Path(destination).chmod(0o600)

    def caddy(self):
        return json.loads(self.run(['docker', 'exec', CADDY, 'wget', '-qO-', 'http://127.0.0.1:2019/config/']))

    def load_caddy(self, config):
        assert_codex_only(self.caddy(), config)
        self.run(['docker', 'exec', '-i', CADDY, 'wget', '-qO-', '--header=Content-Type:application/json',
                  '--post-file=/dev/stdin', 'http://127.0.0.1:2019/load'], json.dumps(config).encode())

    def event(self, name):
        self.events.append({'phase': name, 'time': time.time()})
        private_write(self.directory / 'release.json', json.dumps({'manifest': self.manifest,
            'expected': self.expected(), 'events': self.events, 'opened': self.opened,
            'writer_started': self.writer_started,
            'active_compose': str(self.compose), 'host_port': self.host_port,
            'rollback_image': getattr(self, 'rollback_image', None)}, indent=2))
        print(name, flush=True)

    def request(self, path, public=False):
        base = 'https://' + HOST if public else 'http://127.0.0.1:' + str(self.host_port)
        headers = {'X-Admin-Key': self.env['ADMIN_SECRET'], 'User-Agent': 'Codex2API-MaintenanceCheck/1.0'}
        with urllib.request.urlopen(urllib.request.Request(base + path, headers=headers), timeout=15) as response:
            return response.headers.get('Content-Type', ''), response.read()

    def json_request(self, path, public=False):
        content_type, payload = self.request(path, public)
        if 'application/json' not in content_type:
            raise RuntimeError('smoke endpoint did not return JSON: ' + path)
        return json.loads(payload)

    def protected_containers(self):
        names = self.run(['docker', 'ps', '-a', '--format', '{{.Names}}']).decode().splitlines()
        result = {}
        for name in names:
            if name in [APP, self.migration_name, self.probe_name]:
                continue
            value = self.inspect(name)
            result[name] = {'id': value['Id'], 'started': value['State']['StartedAt'],
                'config': container_config_fingerprint(value)}
        return result

    def verify_protected(self):
        if self.protected_containers() != self.protected:
            raise RuntimeError('non-Codex container/config identity changed')

    def retained(self, database=None, schemas=None):
        result = {}
        queries = {'intelligence_settings': 'SELECT row_to_json(t) FROM intelligence_check_settings t',
                   'intelligence_flags': 'SELECT account_id,enabled FROM intelligence_check_accounts',
                   'account_enabled': 'SELECT id,enabled,locked FROM accounts'}
        for key, query in queries.items():
            rows = self.sql("BEGIN READ ONLY; SELECT COALESCE(json_agg(t)::text,'[]') FROM (" + query + ') t; COMMIT;', database)
            result[key] = fingerprint(json.loads(rows))
        # Exact stable business intent; volatile worker/account runtime fields excluded.
        optional = {
            'system_settings': "SELECT to_jsonb(t)-'updated_at' FROM system_settings t",
            'api_keys': "SELECT to_jsonb(t)-'last_used_at'-'updated_at'-'quota_used'-'total_used'-'last_reset_at'-'reset_count' FROM api_keys t",
            'account_groups': "SELECT to_jsonb(t)-'updated_at' FROM account_groups t",
            'account_group_members': 'SELECT to_jsonb(t) FROM account_group_members t',
            'proxies': "SELECT to_jsonb(t)-'updated_at'-'last_check_at'-'last_latency_ms' FROM proxies t",
            'credential_operations_settings': 'SELECT to_jsonb(t) FROM credential_operations_settings t',
            'credential_operations_login_configs': "SELECT to_jsonb(t)-'updated_at' FROM credential_operations_login_configs t",
            'credential_operations_rules': 'SELECT to_jsonb(t) FROM credential_operations_rules t',
            'credential_operations_monitors': 'SELECT account_id,enabled,auto_relogin_enabled FROM credential_operations_monitors',
            'fixed_egress_config': 'SELECT to_jsonb(t) FROM fixed_egress_config t'}
        for table, query in optional.items():
            if self.sql("SELECT to_regclass('public." + table + "') IS NOT NULL", database) == 't':
                rows = self.sql("BEGIN READ ONLY; SELECT COALESCE(json_agg(t)::text,'[]') FROM (" + query + ') t; COMMIT;', database)
                decoded = json.loads(rows)
                # SELECT scalar creates a to_jsonb column; unwrap it for additive-schema comparison.
                decoded = [row.get('?column?', row.get('to_jsonb', row)) for row in decoded]
                if schemas is None:
                    fields = sorted({key for row in decoded for key in row})
                else:
                    fields = schemas.get(table, [])
                if table == 'api_keys':
                    fields = api_key_config_fields(fields)
                result[table] = project_rows(decoded, fields)
                result.setdefault('_schemas', {})[table] = fields
        result['history_floor'] = [int(x) for x in self.sql('SELECT count(*),COALESCE(max(id),0) FROM intelligence_check_runs', database).split('|')]
        return result

    def verify_retained(self, before, database=None):
        after = self.retained(database, schemas=before.get('_schemas'))
        for key in before:
            if key not in ['history_floor', '_schemas'] and after.get(key) != before[key]:
                raise RuntimeError('preserved configuration changed: ' + key)
        if any(a < b for a, b in zip(after['history_floor'], before['history_floor'])):
            raise RuntimeError('intelligence history was lost')

    def preflight(self):
        # These live-identity reads precede every artifact/config/image mutation.
        self.old = self.check_identity()
        self.project, self.root, active = discover_compose(self.old)
        self.source_path = pathlib.Path(SOURCE_RELEASE)
        self.source_before = self.source_bytes()
        current_source = json.loads(self.source_before) if self.source_before is not None else None
        verify_source_baseline(current_source, self.manifest, self.args.expected_binary_sha256)
        self.compose = pathlib.Path(active)
        self.internal_port, self.host_port = discover_port(self.old)
        self.env = dict(x.split('=', 1) for x in self.old['Config']['Env'])
        if self.env.get('DATABASE_SCHEMA', '') not in ['', 'public']:
            raise ValueError('this release tool requires the public database schema')
        if not self.env.get('ADMIN_SECRET'):
            raise ValueError('environment admin credential required')
        verify_image(self.inspect(self.manifest['image']), self.manifest)
        verify_binary_file(self.manifest)
        self.migration_name = 'codex2api-migrate-' + self.args.release_id
        self.probe_name = 'codex2api-artifact-' + self.args.release_id
        self.protected = self.protected_containers()
        self.original_caddy = self.caddy(); codex_route(self.original_caddy)
        pg = self.inspect(PG)
        pg_env = dict(x.split('=', 1) for x in pg['Config']['Env'])
        if pg_env.get('POSTGRES_DB') != self.args.expected_database_name or pg_env.get('POSTGRES_USER') != self.args.expected_database_user:
            raise ValueError('PostgreSQL service default database differs from expected target')
        self.original_compose = self.compose.read_bytes()
        self.config = json.loads(self.dc('config', '--format', 'json'))
        service = self.config['services'][APP]
        verify_service(self.old, service)
        if self.env.get('DATABASE_NAME') != self.args.expected_database_name or self.env.get('DATABASE_USER') != self.args.expected_database_user:
            raise ValueError('application database target differs from explicit expected target')
        identity = self.sql('BEGIN READ ONLY; SELECT current_database(),current_user; COMMIT;')
        if identity != self.args.expected_database_name + '|' + self.args.expected_database_user:
            raise ValueError('database session identity differs from application target')
        networks = list(self.old['NetworkSettings']['Networks'])
        if len(networks) != 1:
            raise ValueError('single app network required')
        self.network = networks[0]
        network = json.loads(self.run(['docker', 'network', 'inspect', self.network]))[0]
        subnets = [x['Subnet'] for x in network['IPAM']['Config'] if ipaddress.ip_network(x['Subnet']).version == 4]
        if len(subnets) != 1:
            raise ValueError('single IPv4 subnet required')
        self.subnet = subnets[0]; self.gated = False
        self.run(['iptables', '-S', 'DOCKER-USER'])
        self.json_request('/health')
        self.worker_required = bool(self.env.get('CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR'))
        self.verify_worker_enabled_configuration()
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.log = self.directory / 'commands.log'
        for name, value in [('compose.original', self.original_compose), ('compose.resolved.json', json.dumps(self.config)),
                            ('container.original.json', json.dumps(self.old)), ('caddy.original.json', json.dumps(self.original_caddy)),
                            ('protected.original.json', json.dumps(self.protected))]:
            private_write(self.directory / name, value)
        if self.source_before is not None:
            private_write(self.directory / 'source-release.original.json', self.source_before)
        private_write(self.directory / 'source-release.baseline.json', json.dumps(current_source))
        private_write(self.directory / 'build-manifest.json', json.dumps(self.manifest, indent=2))
        self.event('identity-and-configuration-verified')

    def verify_worker_enabled_configuration(self):
        exists = self.sql("SELECT to_regclass('public.credential_operations_settings') IS NOT NULL")
        if exists == 't':
            raw = self.sql("SELECT value FROM credential_operations_settings WHERE key='runtime'")
            runtime = json.loads(raw) if raw else {'enabled': True, 'engine': 'local_worker'}
            self.worker_required = self.worker_required and runtime.get('enabled', True) and runtime.get('engine', 'local_worker') == 'local_worker'

    def network_gate(self, enable):
        if self.gated == enable:
            return
        rule = ['-d', self.subnet, '-p', 'tcp', '--dport', str(self.internal_port), '-m', 'conntrack',
                '--ctstate', 'NEW', '-m', 'comment', '--comment', 'codex-maintenance-' + self.args.release_id, '-j', 'REJECT']
        self.run(['iptables', '-w', '10'] + (['-I', 'DOCKER-USER', '1'] if enable else ['-D', 'DOCKER-USER']) + rule)
        self.gated = enable

    def prepare_rollback(self):
        payload = (pathlib.Path('/proc') / str(self.inspect(APP)['State']['Pid']) / 'exe').read_bytes()
        if hashlib.sha256(payload).hexdigest() != self.args.expected_binary_sha256:
            raise RuntimeError('executing binary changed before rollback capture')
        context = self.directory / 'rollback-build'; context.mkdir(mode=0o700)
        private_write(context / 'codex2api', payload); (context / 'codex2api').chmod(0o700)
        base_tag = 'codex2api:rollback-base-' + self.args.release_id
        self.run(['docker', 'tag', self.old['Image'], base_tag])
        if self.inspect(base_tag)['Id'] != self.old['Image']:
            raise RuntimeError('rollback base tag differs from expected image')
        private_write(context / 'Dockerfile', 'FROM ' + base_tag + chr(10) + 'COPY codex2api ' + EXE + chr(10))
        tag = 'codex2api:rollback-' + self.args.release_id
        self.run(['docker', 'build', '--network=none', '-t', tag, str(context)], timeout=300)
        self.rollback_image = self.inspect(tag)['Id']
        for image, expected in [(self.rollback_image, self.args.expected_binary_sha256),
                                (self.manifest['digest'], self.manifest['binary_sha256'])]:
            digest = self.run(['docker', 'run', '--rm', '--network=none', '--entrypoint=sha256sum', image, EXE]).decode().split()[0]
            if digest != expected:
                raise RuntimeError('image executable digest mismatch')
        private_write(self.directory / 'compose.rollback.json', json.dumps(with_image(self.config, self.rollback_image)))
        self.event('exact-live-binary-rollback-image-verified')

    def rehearse(self):
        self.dump(self.directory / 'preflight.dump')
        scratch = 'codex_rehearse_' + re.sub('[^a-z0-9]', '', self.args.release_id.lower())[-24:]
        self.run(['docker', 'exec', PG, 'sh', '-c', 'exec createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$1"', 'sh', scratch])
        try:
            self.run(['docker', 'exec', '-i', PG, 'sh', '-c',
                'exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$1"', 'sh', scratch],
                (self.directory / 'preflight.dump').read_bytes(), timeout=300)
            before = self.retained(scratch)
            env = self.directory / 'rehearsal.env'
            # migrate-only returns before cache initialization and worker startup.
            values = rehearsal_environment(self.env, scratch)
            private_write(env, chr(10).join(k + '=' + v for k, v in values.items()) + chr(10))
            self.run(['docker', 'run', '--rm', '--name', self.probe_name, '--network', self.network,
                '--env-file', str(env), '-e', 'CODEX_MIGRATE_ONLY=1', self.manifest['digest']], timeout=600)
            self.verify_retained(before, scratch)
            self.event('isolated-database-migration-rehearsal-passed')
        finally:
            if self.run(['docker', 'ps', '-aq', '--filter', 'name=^' + self.probe_name + '$']).strip():
                self.run(['docker', 'rm', '-f', self.probe_name])
            self.run(['docker', 'exec', PG, 'sh', '-c', 'exec dropdb --force -U "$POSTGRES_USER" "$1"', 'sh', scratch])
            (self.directory / 'rehearsal.env').unlink(missing_ok=True)

    def drain(self):
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            value = self.json_request('/api/admin/runtime-status').get('accounts', {}).get('active_requests')
            if isinstance(value, int) and not isinstance(value, bool) and value == 0:
                return max(0, int(deadline - time.monotonic()))
            time.sleep(min(2, max(0, deadline - time.monotonic())))
        raise RuntimeError('active requests did not drain; no app was stopped')

    def ready(self):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            try:
                if self.json_request('/health').get('status') == 'ok':
                    return
            except Exception:
                pass
            time.sleep(1)
        raise RuntimeError('application readiness timeout')

    def smoke(self, public=False):
        if self.json_request('/health', public).get('build_version') != self.manifest['build_version']:
            raise RuntimeError('build version mismatch')
        for path in ['/api/admin/settings', '/api/admin/intelligence-check/settings',
                     '/api/admin/intelligence-check/accounts', '/api/admin/intelligence-check/runs',
                     '/api/admin/credential-ops/encryption']:
            self.json_request(path, public)
        ids = [int(x) for x in self.sql("SELECT id FROM accounts WHERE status<>'deleted' ORDER BY id LIMIT 3").splitlines()]
        if not ids:
            raise RuntimeError('no eligible account IDs available for quota smoke')
        validate_quota_stats(self.json_request('/api/admin/accounts/page-stats?ids=' + ','.join(str(x) for x in ids), public), ids)
        for path in ['/admin/accounts', '/admin/intelligence-check', '/admin/credential-ops']:
            if b'<html' not in self.request(path, public)[1].lower():
                raise RuntimeError('admin UI shell missing')
        if not public:
            if self.binary_hash() != self.manifest['binary_sha256']:
                raise RuntimeError('running executable differs from release')
            if self.worker_required:
                deadline = time.monotonic() + 90
                while True:
                    try:
                        worker = self.json_request('/api/admin/credential-ops').get('worker', {})
                        timestamp = datetime.datetime.fromisoformat(worker.get('last_heartbeat', '').replace('Z', '+00:00')).timestamp()
                        api_age = time.time() - timestamp
                        age = self.run(['docker', 'exec', APP, 'sh', '-c',
                            'test -f /tmp/credential-worker-heartbeat && now=$(date +%s); stamp=$(stat -c %Y /tmp/credential-worker-heartbeat); printf "%s" "$((now-stamp))"']).decode().strip()
                        if worker.get('online') is True and 0 <= api_age < 90 and 0 <= int(age) < 45:
                            break
                    except Exception:
                        pass
                    if time.monotonic() >= deadline:
                        raise RuntimeError('embedded credential worker heartbeat missing/stale')
                    time.sleep(2)
        self.verify_retained(self.retained_before)
        self.verify_protected()

    def restore_route(self):
        current = self.caddy()
        assert_codex_only(self.original_caddy, current)
        codex_route(current)['handle'] = copy.deepcopy(codex_route(self.original_caddy)['handle'])
        self.load_caddy(current); self.maintenance = False

    def rollback(self):
        self.event('rollback-started'); self.network_gate(True)
        self.load_caddy(maintenance_config(self.caddy())); self.maintenance = True
        if self.opened:
            self.drain()
        if self.run(['docker', 'ps', '-aq', '--filter', 'name=^' + self.migration_name + '$']).strip():
            self.run(['docker', 'rm', '-f', self.migration_name])
        self.dc('stop', '-t', '60', APP, timeout=90)
        # Full app startup launches credential/intelligence writers behind ingress.
        # No old dump or old app is safe after this boundary without separately
        # verified backward compatibility. Keep latest DB and maintenance instead.
        if self.writer_started or self.opened:
            self.event('recovery-required-current-database-preserved')
            raise RuntimeError('candidate writers may have committed changes; current database retained, app stopped and maintenance active; operator recovery required')
        if self.migrated:
            self.run(['docker', 'exec', PG, 'sh', '-c',
                'dropdb --force -U "$POSTGRES_USER" "$POSTGRES_DB" && createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$POSTGRES_DB"'])
            self.run(['docker', 'exec', '-i', PG, 'sh', '-c',
                'exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],
                (self.directory / 'stopped.dump').read_bytes(), timeout=300)
        atomic_write(self.compose, json.dumps(with_image(self.config, self.rollback_image)).encode())
        self.dc('up', '-d', '--no-deps', '--force-recreate', APP); self.ready()
        if self.binary_hash() != self.args.expected_binary_sha256:
            raise RuntimeError('rollback exact binary verification failed; retain maintenance')
        self.verify_protected(); self.restore_source_record(); self.restore_route(); self.network_gate(False)
        self.event('rollback-completed-with-current-writes' if self.opened else 'rollback-completed-before-open')

    def deploy(self):
        self.preflight(); self.prepare_rollback(); self.rehearse()
        self.check_identity(); self.check_source_unchanged(); self.verify_protected()
        try:
            self.network_gate(True)
            self.load_caddy(maintenance_config(self.caddy())); self.maintenance = True
            self.event('maintenance-on')
            grace = self.drain(); self.check_identity(); self.check_source_unchanged()
            self.dc('stop', '-t', str(grace), APP, timeout=grace + 30); self.stopped = True
            self.event('single-app-and-embedded-worker-stopped')
            self.dump(self.directory / 'stopped.dump')
            self.retained_before = self.retained()
            private_write(self.directory / 'retained.before.json', json.dumps(self.retained_before))
            atomic_write(self.compose, json.dumps(with_image(self.config, self.manifest['digest'])).encode())
            self.dc('config', '--quiet'); self.migrated = True
            self.dc('run', '--rm', '--name', self.migration_name, '--no-deps', '-e', 'CODEX_MIGRATE_ONLY=1', APP, timeout=600)
            self.verify_retained(self.retained_before)
            # Set before the command: a timeout/error may still start writers.
            self.writer_started = True
            self.event('candidate-writer-start-boundary')
            self.dc('up', '-d', '--no-deps', '--force-recreate', APP); self.ready()
            self.smoke(); self.event('internal-version-quota-intelligence-worker-smoke-passed')
            self.opened = True; self.restore_route(); self.network_gate(False); self.event('traffic-opened')
            self.smoke(public=True)
            self.publish_source_record(); self.event('source-release-record-persisted')
            self.event('release-success')
        except BaseException:
            self.event('release-failed')
            if self.stopped:
                self.rollback()
            else:
                if self.maintenance:
                    self.restore_route()
                self.network_gate(False)
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['rehearse', 'deploy'])
    for key in ['manifest', 'backup', 'release-id', 'expected-container-id', 'expected-image-id', 'expected-binary-sha256', 'expected-database-name', 'expected-database-user']:
        parser.add_argument('--' + key, required=True)
    args = parser.parse_args()
    if not re.fullmatch('[a-zA-Z0-9._-]{1,80}', args.release_id) or not pathlib.Path(args.backup).is_absolute():
        parser.error('valid release id and absolute private backup path required')
    os.umask(0o077)
    with release_lock():
        tool = MaintenanceRelease(args)
        if args.action == 'deploy':
            tool.deploy()
        else:
            tool.preflight(); tool.prepare_rollback(); tool.rehearse(); tool.verify_protected()


if __name__ == '__main__':
    main()
