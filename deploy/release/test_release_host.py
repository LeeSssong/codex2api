import copy, unittest
from release_host import maintenance_config, replace_image, check_source
class ReleaseTests(unittest.TestCase):
 def test_maintenance_only_changes_codex_route(self):
  config={'apps':{'http':{'servers':{'srv0':{'routes':[{'match':[{'host':['api.xingqiaolab.top']}],'handle':[{'handler':'reverse_proxy'}]},{'match':[{'host':['codex.xingqiaolab.top','64-83-10-67.nip.io']}],'handle':[{'handler':'subroute'}]}]}}}}}
  original=copy.deepcopy(config); updated=maintenance_config(config)
  self.assertEqual(config,original)
  routes=updated['apps']['http']['servers']['srv0']['routes']
  self.assertEqual(routes[0],original['apps']['http']['servers']['srv0']['routes'][0]);self.assertEqual(routes[1]['handle'][0]['status_code'],503)
 def test_missing_route_fails_closed(self):
  with self.assertRaises(ValueError):maintenance_config({'apps':{'http':{'servers':{}}}})
 def test_compose_changes_only_app_image(self):
  before='services:\n  codex2api:\n    image: old\n    env_file: .env\n  postgres:\n    image: postgres:18\n'
  self.assertEqual(replace_image(before,'new'),before.replace('image: old','image: new'))
  with self.assertRaises(ValueError):replace_image('services: {}','new')
 def test_image_source_is_checked(self):
  labels={'org.opencontainers.image.revision':'a','io.xingqiao.source-tree':'b'}
  check_source(labels,'a','b')
  with self.assertRaises(ValueError):check_source(labels,'wrong','b')

class RollbackDecisionTests(unittest.TestCase):
 def fake(self, failure):
  import pathlib, types
  from release_host import Release
  class Fake(Release):
   def __init__(self):
    self.args=types.SimpleNamespace(image='new',digest='digest',release_id='test');self.before='services:\n  codex2api:\n    image: old\n';self.dir=pathlib.Path('/unused');self.report={};self.maintenance=False;self.stopped=False;self.opened=False;self.migrated=False;self.calls=[]
   def preflight(self):self.original_settings={'codex_basispoints_enabled':False}
   def event(self,n):self.calls.append(n)
   def save(self):pass
   def caddy(self):return {'apps':{'http':{'servers':{'s':{'routes':[{'match':[{'host':['codex.xingqiaolab.top']}],'handle':[]}]}}}}}
   def load_caddy(self,c):pass
   def dc(self,*args,**kw):
    if args[0]=='run' and failure=='migration':raise RuntimeError('migration failed')
   def dump(self,p):pass
   def write_compose(self,c):pass
   def ready(self):pass
   def inspect(self,n):return {'Image':'digest'}
   def request(self,p,auth=False,public=False):
    if public and failure=='public':raise RuntimeError('public failed')
    if p.endswith('/module'):return 200,{},b'{"enabled":false}'
    if p.endswith('/settings'):return 200,{},b'{"codex_basispoints_enabled":false}'
    return 200,{},b'<html></html>'
   def restore_route(self):self.maintenance=False
   def rollback(self,preserve_database=False):self.calls.append(('rollback',preserve_database))
  return Fake()
 def test_failed_migration_restores_database_before_traffic(self):
  r=self.fake('migration')
  with self.assertRaises(RuntimeError):r.execute()
  self.assertIn(('rollback',False),r.calls);self.assertFalse(r.opened)
 def test_public_failure_preserves_resumed_database_writes(self):
  r=self.fake('public')
  with self.assertRaises(RuntimeError):r.execute()
  self.assertIn(('rollback',True),r.calls);self.assertTrue(r.opened)

class MigratorCleanupTests(unittest.TestCase):
 def test_orphan_migrator_removed_before_recovery(self):
  import types
  from release_host import Release
  r=Release.__new__(Release);r.args=types.SimpleNamespace(release_id='test');calls=[];responses=iter([b'id',b'',b''])
  def run(cmd,**kwargs):calls.append(cmd);return next(responses)
  r.run=run;r.stop_migrator()
  self.assertEqual(calls[1],['docker','rm','-f','codex2api-migrate-test'])
 def test_migrator_cleanup_failure_blocks_database_recovery(self):
  import types
  from release_host import Release
  r=Release.__new__(Release);r.args=types.SimpleNamespace(release_id='test');r.run=lambda *a,**k:b'still-running'
  with self.assertRaises(RuntimeError):r.stop_migrator()
if __name__=='__main__':unittest.main()
