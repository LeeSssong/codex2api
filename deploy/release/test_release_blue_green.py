import copy,tempfile,unittest
from pathlib import Path
from release_blue_green import change_upstream,change_caddyfile_upstream,caddyfile_bind_source,persist_caddyfile

class RoutingTests(unittest.TestCase):
 def test_changes_only_codex_upstream(self):
  source={'apps':{'http':{'servers':{'main':{'routes':[
   {'match':[{'host':['codex.xingqiaolab.top']}],'handle':[{'handler':'reverse_proxy','upstreams':[{'dial':'172.18.0.1:18080'}]}]},
   {'match':[{'host':['api.xingqiaolab.top']}],'handle':[{'handler':'reverse_proxy','upstreams':[{'dial':'sub:8080'}]}]},
  ]}}}}}
  before=copy.deepcopy(source);result=change_upstream(source,18080,18081)
  self.assertEqual(source,before)
  routes=result['apps']['http']['servers']['main']['routes']
  self.assertEqual(routes[0]['handle'][0]['upstreams'][0]['dial'],'172.18.0.1:18081')
  self.assertEqual(routes[1],before['apps']['http']['servers']['main']['routes'][1])
  with self.assertRaises(ValueError):change_upstream(source,9999,18081)

 def test_caddyfile_changes_only_codex_site_and_preserves_other_sites(self):
  source='''codex.xingqiaolab.top {\n    reverse_proxy 172.18.0.1:18080\n}\n\napi.xingqiaolab.top {\n    reverse_proxy sub2api-api:8080\n}\n'''
  result=change_caddyfile_upstream(source,18080,18081)
  self.assertIn('reverse_proxy 172.18.0.1:18081',result)
  self.assertIn('reverse_proxy sub2api-api:8080',result)
  self.assertEqual(source.split('api.xingqiaolab.top')[1],result.split('api.xingqiaolab.top')[1])
  with self.assertRaises(ValueError):change_caddyfile_upstream(source,9999,18081)

 def test_bind_source_supports_file_and_directory_mounts(self):
  self.assertEqual(caddyfile_bind_source([{'Type':'bind','Source':'/opt/sub2api/production/Caddyfile','Destination':'/etc/caddy/Caddyfile'}]),Path('/opt/sub2api/production/Caddyfile'))
  self.assertEqual(caddyfile_bind_source([{'Type':'bind','Source':'/opt/sub2api/production','Destination':'/etc/caddy'}]),Path('/opt/sub2api/production/Caddyfile'))

 def test_persist_caddyfile_atomically_replaces_host_source_and_backs_up(self):
  with tempfile.TemporaryDirectory() as folder:
   path=Path(folder)/'Caddyfile';backup=Path(folder)/'before';path.write_text('codex.xingqiaolab.top { reverse_proxy 127.0.0.1:18080 }\n')
   inode=path.stat().st_ino
   persist_caddyfile(path,18080,18081,backup)
   self.assertNotEqual(path.stat().st_ino,inode)
   self.assertIn(':18081',path.read_text());self.assertIn(':18080',backup.read_text())

if __name__=='__main__':unittest.main()
