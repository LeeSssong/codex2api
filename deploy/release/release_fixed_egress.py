#!/usr/bin/env python3
"""Additive fixed-egress release; preserve existing routes and default-off state.

Run on production with image source labels checked by BlueGreenRelease. Take a
restorable database backup, verify management counts, then switch only Codex.
This module never calls a proxy supplier or modifies account enrollment.
"""
import argparse
import fcntl
import json
import os
import re
from release_blue_green import BlueGreenRelease


def management_counts():
    return ('accounts', 'api_keys', 'proxies', 'account_groups', 'account_group_members')


def verify_fixed_egress_snapshot(value):
    if not isinstance(value, dict) or value.get('config', {}).get('enabled') is not False:
        raise ValueError('fixed egress must be explicitly disabled on initial rollout')
    for field in ('pools', 'providers', 'resources', 'bindings'):
        if value.get(field) != []:
            raise ValueError('unexpected fixed-egress inventory on initial rollout: ' + field)


def rehearsal_environment(values, database_name):
    values = dict(values, DATABASE_NAME=database_name, CODEX_MIGRATE_ONLY='1')
    if any('\n' in value or '\r' in value for value in values.values()):
        raise ValueError('multiline environment cannot be serialized safely')
    return ''.join(key + '=' + values[key] + '\n' for key in sorted(values))


class FixedEgressRelease(BlueGreenRelease):
    def ready(self):
        super().ready()
        value = json.loads(self.request('/api/admin/fixed-egress', True)[2])
        verify_fixed_egress_snapshot(value)
        for path in ('/api/admin/ipv6-egress', '/api/admin/model-quality', '/api/admin/state-pool'):
            status, headers, payload = self.request(path, True)
            if status != 200 or not isinstance(json.loads(payload), dict):
                raise RuntimeError('candidate retained endpoint failed: ' + path)
        self.report['fixed_egress_default_off_verified'] = True
        self.save()

    def request(self, path, auth=False, public=False, timeout=10):
        response = super().request(path, auth, public, timeout)
        # BlueGreenRelease calls this after setting switched=True and before
        # draining the old app, so a public feature failure takes its rollback.
        if public and path == '/health':
            health = json.loads(response[2])
            if health.get('build_version') == 'release-' + self.args.release_id:
                verify_fixed_egress_snapshot(json.loads(super().request('/api/admin/fixed-egress', True, True, timeout)[2]))
        return response

    def rehearse_migration(self):
        scratch = 'codex_fixed_verify_' + re.sub('[^a-z0-9]', '', self.args.release_id.lower())[-20:]
        self.run(['docker', 'exec', 'codex2api-postgres', 'sh', '-c',
                  'exec createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$1"', 'sh', scratch])
        envfile = self.dir / 'rehearsal.env'
        try:
            self.run(['docker', 'exec', '-i', 'codex2api-postgres', 'sh', '-c',
                      'exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$1"', 'sh', scratch],
                     (self.dir / 'preflight.dump').read_bytes())
            envfile.write_text(rehearsal_environment(self.env, scratch))
            envfile.chmod(0o600)
            self.run(['docker', 'run', '--rm', '--network', 'codex2api-net',
                      '--env-file', str(envfile), self.args.image], timeout=240)
            for table, before in self.report['management_counts_before'].items():
                if self.sql('SELECT count(*) FROM ' + table, scratch) != before:
                    raise RuntimeError('migration rehearsal changed management count: ' + table)
            self.event('migration-rehearsal-passed')
        finally:
            envfile.unlink(missing_ok=True)
            self.run(['docker', 'exec', 'codex2api-postgres', 'sh', '-c',
                      'exec dropdb -U "$POSTGRES_USER" "$1"', 'sh', scratch])

    def execute(self):
        # Capture existing business-record counts, not continuously growing logs.
        self.report['management_counts_before'] = {
            table: self.sql('SELECT count(*) FROM ' + table)
            for table in management_counts()
        }
        self.dump(self.dir / 'preflight.dump')
        self.event('database-backup-verified')
        self.rehearse_migration()
        super().execute()
        verify_fixed_egress_snapshot(json.loads(self.request('/api/admin/fixed-egress', True, public=True)[2]))
        for path in ('/api/admin/ipv6-egress', '/api/admin/model-quality', '/api/admin/state-pool'):
            status, headers, payload = self.request(path, True, public=True)
            if status != 200 or not isinstance(json.loads(payload), dict):
                raise RuntimeError('retained endpoint failed: ' + path)
        after = {table: self.sql('SELECT count(*) FROM ' + table) for table in management_counts()}
        self.report['management_counts_after'] = after
        if after != self.report['management_counts_before']:
            self.report['management_count_drift'] = True
            self.save()
            raise RuntimeError('management record counts changed during release; inspect release evidence')
        self.verify_protected_containers()
        self.event('fixed-egress-public-smoke-passed')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    for name in ('image', 'digest', 'revision', 'tree', 'release-id'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    with open('/var/lock/codex2api-release.lock', 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        FixedEgressRelease(args).execute()
