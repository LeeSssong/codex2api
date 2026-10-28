import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location('ipv6_pool', pathlib.Path(__file__).with_name('ipv6_pool.py'))
pool = importlib.util.module_from_spec(spec)
if spec.loader and spec.origin and pathlib.Path(spec.origin).exists():
    spec.loader.exec_module(pool)


class PoolTests(unittest.TestCase):
    def config(self):
        return {'prefix': '2a0a:4cc0:101:799:c2a:28::/112', 'count': 128,
                'parent': 'eth0', 'interface': 'c2a-v6', 'gateway': 'fe80::1'}

    def test_addresses_are_public_bounded_and_primary_not_selected(self):
        cfg = pool.validate_config(self.config())
        self.assertEqual(cfg['addresses'][0], '2a0a:4cc0:101:799:c2a:28:0:1')
        self.assertEqual(cfg['addresses'][-1], '2a0a:4cc0:101:799:c2a:28:0:80')
        for patch in [{'prefix': 'fd00::/112'}, {'prefix': '2a0a:4cc0::/64'},
                      {'count': 0}, {'count': 4097}, {'parent': 'eth0;bad'},
                      {'interface': 'eth0'}, {'gateway': '2001:4860::1'}]:
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                pool.validate_config(dict(self.config(), **patch))

    def test_only_exact_running_compose_app_namespace_is_accepted(self):
        c = {'Id': 'a'*64, 'Name': '/codex2api', 'State': {'Pid': 432, 'Running': True},
             'Config': {'Labels': {'com.docker.compose.project': 'codex2api', 'com.docker.compose.service': 'codex2api'}}}
        self.assertEqual(pool.container_pid(c), 432)
        for key, value in [('Name', '/sub2api'), ('State', {'Pid': 1, 'Running': True})]:
            with self.assertRaises(ValueError):
                pool.container_pid(dict(c, **{key: value}))
        c['Config']['Labels']['com.docker.compose.project'] = 'sub2api'
        with self.assertRaises(ValueError): pool.container_pid(c)

    def test_existing_interface_must_match_parent_link_and_exact_pool(self):
        cfg=pool.validate_config(self.config())
        link={'ifname':'c2a-v6','linkinfo':{'info_kind':'ipvlan','info_data':{'mode':'l2'}}}
        pool.validate_link(link)
        with self.assertRaises(ValueError): pool.validate_link(link, 2)
        with self.assertRaises(ValueError): pool.validate_link({'linkinfo':{'info_kind':'veth'}})
        addr={'addr_info':[{'family':'inet6','local':cfg['addresses'][0], 'prefixlen':64,'scope':'global','flags':[]}]}
        self.assertEqual(pool.assigned_addresses(addr), {cfg['addresses'][0]})
        addr['addr_info'][0]['flags']=['dadfailed']
        with self.assertRaises(RuntimeError):pool.assigned_addresses(addr)

    def test_route_scope_does_not_change_ipv4_or_unrelated_ipv6_routes(self):
        cfg=pool.validate_config(self.config())
        commands=pool.address_commands(cfg, {cfg['addresses'][0]}, [])
        self.assertEqual(sum('addr' in cmd for cmd in commands),127)
        self.assertTrue(all(cmd[:2]==['ip','-6'] for cmd in commands))
        self.assertIn(['ip','-6','route','add','default','via','fe80::1','dev','c2a-v6','metric','2048'],commands)
        self.assertFalse(any('replace' in cmd or 'flush' in cmd for cmd in commands))
        routes=[{'dst':'default','gateway':'fe80::1','dev':'c2a-v6','metric':2048}]
        self.assertEqual(pool.address_commands(cfg,set(cfg['addresses']),routes),[])
        with self.assertRaises(ValueError):pool.address_commands(cfg,set(cfg['addresses'])|{'2001:4860::1'},routes)

if __name__ == '__main__': unittest.main()
