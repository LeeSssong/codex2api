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
        with self.assertRaises(ValueError):
            pool.container_pid(dict(c, HostConfig={'NetworkMode':'host'}))
        with self.assertRaises(ValueError):
            pool.container_pid(dict(c, HostConfig={'NetworkMode':'container:other'}))
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
        addr['addr_info'][0]['flags']=[]
        addr['addr_info'][0]['tentative']=True
        with self.assertRaises(RuntimeError):pool.assigned_addresses(addr)
        addr['addr_info'][0]['tentative']=False
        addr['addr_info'][0]['dadfailed']=True
        with self.assertRaises(RuntimeError):pool.assigned_addresses(addr)

    def test_same_gateway_router_advertisement_coexists_with_static_route(self):
        cfg=pool.validate_config(self.config())
        ra={'dst':'default','gateway':'fe80::1','dev':'c2a-v6','protocol':'ra','metric':1024,'expires':1797}
        static={'dst':'default','gateway':'fe80::1','dev':'c2a-v6','metric':2048}
        self.assertEqual(pool.address_commands(cfg,set(cfg['addresses']),[ra,static]),[])
        self.assertEqual(pool.address_commands(cfg,set(cfg['addresses']),[ra]),[['ip','-6','route','add','default','via','fe80::1','dev','c2a-v6','metric','2048']])
        with self.assertRaises(ValueError):
            pool.address_commands(cfg,set(cfg['addresses']),[dict(ra,gateway='fe80::2'),static])
        with self.assertRaises(ValueError):
            pool.address_commands(cfg,set(cfg['addresses']),[dict(static,metric=1024)])

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

class NamespaceMutationTests(unittest.TestCase):
    def test_unexpected_existing_pool_is_rejected_before_link_is_raised(self):
        from unittest import mock
        from types import SimpleNamespace
        import json
        cfg={'prefix':'2a0a:4cc0:101:799:c2a:28::/112','count':128,'parent':'eth0','interface':'c2a-v6','gateway':'fe80::1'}
        c={'Id':'a'*64,'Name':'/codex2api','State':{'Running':True,'Pid':432},'Config':{'Labels':{'com.docker.compose.project':'codex2api','com.docker.compose.service':'codex2api'}}}
        calls=[]
        def command(args,**kwargs):
            calls.append(args)
            if args[0]=='ip': return json.dumps([{'flags':['UP'],'ifindex':2}]).encode()
            if 'link' in args and 'show' in args:
                return json.dumps([{'link_index':2,'flags':[], 'linkinfo':{'info_kind':'ipvlan','info_data':{'mode':'l2'}}}]).encode()
            if 'addr' in args:
                return json.dumps([{'addr_info':[{'family':'inet6','local':'2001:4860::1','prefixlen':64,'scope':'global'}]}]).encode()
            return b'[]'
        with mock.patch.object(pool,'inspect_app',return_value=c),mock.patch.object(pool,'command',side_effect=command),mock.patch.object(pool.os,'open',return_value=99),mock.patch.object(pool.os,'close'),mock.patch.object(pool.os,'stat',return_value=SimpleNamespace(st_dev=1,st_ino=1)),mock.patch.object(pool.os,'fstat',return_value=SimpleNamespace(st_dev=1,st_ino=2)):
            with self.assertRaises(ValueError):pool.ensure(cfg)
        self.assertFalse(any('up' in cmd for cmd in calls))
