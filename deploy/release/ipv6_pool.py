#!/usr/bin/env python3
"""Reconcile only codex2api's dedicated ipvlan namespace; never alter Docker/host routes.

Install beside a root-owned JSON config. `watch` survives app recreation/reboot;
`ensure` is a release readiness gate. IPv4 and unrelated containers are untouched.
"""
import argparse
import fcntl
import ipaddress
import json
import logging
import os
import pathlib
import re
import subprocess
import time


def validate_config(raw):
    cfg = dict(raw)
    prefix = ipaddress.ip_network(cfg['prefix'], strict=True)
    count = cfg.get('count', 128)
    if prefix.version != 6 or not prefix.network_address.is_global or prefix.prefixlen != 112:
        raise ValueError('dedicated public IPv6 /112 required')
    if type(count) is not int or not 1 <= count <= 4096:
        raise ValueError('pool count must be 1..4096')
    for key in ('parent', 'interface'):
        if not re.fullmatch(r'[a-zA-Z0-9_-]{1,15}', cfg.get(key, '')):
            raise ValueError('invalid interface name')
    if cfg['interface'] in ('lo', cfg['parent']):
        raise ValueError('dedicated interface name required')
    gateway = ipaddress.ip_address(cfg['gateway'])
    if gateway.version != 6 or not gateway.is_link_local:
        raise ValueError('link-local gateway required')
    cfg['addresses'] = [str(prefix.network_address + n) for n in range(1, count + 1)]
    return cfg


def container_pid(container):
    labels = container['Config'].get('Labels') or {}
    state = container['State']
    if (container.get('Name') != '/codex2api' or not state.get('Running') or
        type(state.get('Pid')) is not int or state['Pid'] <= 1 or
        not re.fullmatch('[a-f0-9]{64}', container.get('Id', '')) or
        labels.get('com.docker.compose.project') != 'codex2api' or
        labels.get('com.docker.compose.service') != 'codex2api'):
        raise ValueError('only running Compose codex2api app namespace is allowed')
    return state['Pid']


def validate_link(link, parent_index=None):
    info = link.get('linkinfo', {})
    if info.get('info_kind') != 'ipvlan' or info.get('info_data', {}).get('mode') != 'l2':
        raise ValueError('existing dedicated interface is not ipvlan L2')
    if parent_index is not None and link.get('link_index') != parent_index:
        raise ValueError('existing dedicated interface has a different parent')


def assigned_addresses(link):
    ips = set()
    for addr in link.get('addr_info', []):
        if addr.get('family') != 'inet6' or addr.get('scope') != 'global':
            continue
        if 'dadfailed' in addr.get('flags', []):
            raise RuntimeError('IPv6 duplicate address detection failed')
        if 'tentative' in addr.get('flags', []):
            raise RuntimeError('IPv6 duplicate address detection pending')
        if addr.get('prefixlen') != 64:
            raise ValueError('dedicated addresses must retain uplink /64')
        ips.add(addr['local'])
    return ips


def address_commands(cfg, assigned, routes):
    wanted = set(cfg['addresses'])
    if assigned - wanted:
        raise ValueError('unexpected addresses on dedicated interface; refusing to overwrite')
    commands = [['ip', '-6', 'addr', 'add', ip + '/64', 'dev', cfg['interface']]
                for ip in cfg['addresses'] if ip not in assigned]
    defaults = [r for r in routes if r.get('dst') == 'default' and r.get('dev') == cfg['interface']]
    if any(r.get('gateway') != cfg['gateway'] or r.get('metric') != 2048 for r in defaults):
        raise ValueError('unexpected dedicated default route')
    if not defaults:
        commands.append(['ip', '-6', 'route', 'add', 'default', 'via', cfg['gateway'],
                         'dev', cfg['interface'], 'metric', '2048'])
    return commands


def command(args, optional=False, pass_fds=()):
    p = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                       timeout=15, pass_fds=pass_fds)
    if p.returncode and not optional:
        # Do not log arbitrary docker inspect/env/command output.
        raise RuntimeError('IPv6 namespace command failed: ' + args[0])
    return None if p.returncode else p.stdout


def inspect_app():
    raw = command(['docker', 'inspect', 'codex2api'], optional=True)
    return json.loads(raw)[0] if raw else None


def ensure(cfg):
    cfg = validate_config(cfg)
    container = inspect_app()
    if container is None or not container.get('State', {}).get('Running'):
        return False  # No substitute container; wait during a deployment stop.
    pid = container_pid(container)
    # Hold the actual namespace open throughout reconciliation, avoiding PID reuse
    # when Docker replaces the application between commands.
    fd = os.open('/proc/%d/ns/net' % pid, os.O_RDONLY)
    try:
        namespace = '/proc/self/fd/%d' % fd
        def ns(*args, optional=False):
            return command(['nsenter', '--net=' + namespace, '--'] + list(args),
                           optional=optional, pass_fds=(fd,))
        fresh = inspect_app()
        if fresh is None or fresh['Id'] != container['Id'] or container_pid(fresh) != pid:
            raise RuntimeError('application changed during namespace acquisition')
        current = ns('ip', '-j', '-d', 'link', 'show', 'dev', cfg['interface'], optional=True)
        created = moved = False
        parent = json.loads(command(['ip', '-j', 'link', 'show', 'dev', cfg['parent']]))[0]
        if 'UP' not in parent['flags']:
            raise RuntimeError('uplink is not up')
        try:
            if current is None:
                # A host-side name collision is an error, never deleted/overwritten.
                command(['ip', 'link', 'add', cfg['interface'], 'link', cfg['parent'], 'type', 'ipvlan', 'mode', 'l2'])
                created = True
                command(['ip', 'link', 'set', cfg['interface'], 'netns', namespace], pass_fds=(fd,))
                moved = True
            else:
                validate_link(json.loads(current)[0], parent['ifindex'])
            ns('ip', 'link', 'set', cfg['interface'], 'up')
            assigned = assigned_addresses(json.loads(ns('ip', '-j', '-6', 'addr', 'show', 'dev', cfg['interface']))[0])
            routes = json.loads(ns('ip', '-j', '-6', 'route', 'show', 'default'))
            commands = address_commands(cfg, assigned, routes)
            for args in commands:
                ns(*args)
            if commands:
                # Readiness follows DAD completion, not a fixed deployment sleep.
                deadline = time.monotonic() + 8
                while True:
                    try:
                        actual = assigned_addresses(json.loads(ns('ip', '-j', '-6', 'addr', 'show', 'dev', cfg['interface']))[0])
                        if actual != set(cfg['addresses']):
                            raise RuntimeError('IPv6 pool incomplete')
                        break
                    except RuntimeError:
                        if time.monotonic() >= deadline:
                            raise
                        time.sleep(.2)
            return True
        except BaseException:
            if created:
                # Remove only this attempt's interface, whichever side move reached.
                if moved:
                    ns('ip', 'link', 'del', cfg['interface'], optional=True)
                else:
                    command(['ip', 'link', 'del', cfg['interface']], optional=True)
            raise
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['ensure', 'watch'])
    parser.add_argument('--config', required=True)
    args = parser.parse_args()
    path = pathlib.Path(args.config)
    stat = path.stat()
    if path.is_symlink() or stat.st_uid != 0 or stat.st_mode & 0o022:
        raise ValueError('root-owned config without group/other write required')
    cfg = json.loads(path.read_text())
    validate_config(cfg)
    logging.basicConfig(level=logging.INFO, format='%(asctime)s %(message)s')
    last = None
    while True:
        try:
            # Separate from release lock: release invokes ensure while holding its
            # own lock. This serializes daemon/release namespace mutations.
            with open('/run/lock/codex2api-ipv6-pool.lock', 'a') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                ok = ensure(cfg)
            if args.action == 'ensure':
                if not ok:
                    raise RuntimeError('application is not running')
                print('codex2api IPv6 pool ready: %d addresses' % cfg.get('count', 128))
                return
            status = 'ready' if ok else 'waiting for codex2api'
        except Exception as error:
            if args.action == 'ensure':
                raise
            status = type(error).__name__ + ': ' + str(error)
        if status != last:
            logging.info('%s', status)
            last = status
        time.sleep(2)


if __name__ == '__main__':
    main()
