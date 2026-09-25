import copy, unittest
from release_host import maintenance_config, replace_image, check_source, module_state, assert_unchanged_containers, image_pinned_compose
class ReleaseTests(unittest.TestCase):
 def test_standard_image_uses_persisted_module_flag(self):
  self.assertTrue(module_state(None, 'true'))
  self.assertFalse(module_state(None, None))
  self.assertFalse(module_state({'enabled':False}, 'true'))
 def test_rollback_pins_the_original_image_id(self):
  before='services:\n  codex2api:\n    image: ghcr.io/hloolx/codex2api:latest\n'
  self.assertIn('image: sha256:old',image_pinned_compose(before,{'Image':'sha256:old'}))
 def test_unrelated_containers_are_unchanged(self):
  assert_unchanged_containers({'db':'id1','sub':'id2'},{'db':'id1','sub':'id2'})
  with self.assertRaises(RuntimeError):assert_unchanged_containers({'db':'id1'},{'db':'id3'})
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
    self.args=types.SimpleNamespace(image='new',digest='digest',release_id='test');self.before='services:\n  codex2api:\n    image: old\n';self.dir=pathlib.Path('/unused');self.report={};self.maintenance=False;self.stopped=False;self.opened=False;self.migrated=False;self.app_started=False;self.gated=False;self.calls=[]
   def preflight(self):self.original_settings={'codex_basispoints_enabled':False};self.original_account_ops_enabled=False
   def event(self,n):self.calls.append(n)
   def drain(self):
    import time
    self.drain_started=time.monotonic();self.calls.append('drain')
   def verify_protected_containers(self):pass
   def feature_smoke(self,public=False):self.request('/health',public=public)
   def save(self):pass
   def caddy(self):return {'apps':{'http':{'servers':{'s':{'routes':[{'match':[{'host':['codex.xingqiaolab.top']}],'handle':[]}]}}}}}
   def load_caddy(self,c):pass
   def network_gate(self,v):self.gated=v
   def dc(self,*args,**kw):
    if args[0]=='run' and failure=='migration':raise RuntimeError('migration failed')
   def dump(self,p):pass
   def write_compose(self,c):pass
   def ready(self):
    if failure=='startup':raise RuntimeError('startup failed')
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

class NetworkGateTests(unittest.TestCase):
 def test_gate_is_scoped_to_new_codex_connections(self):
  import types
  from release_host import Release
  r=Release.__new__(Release);r.gated=False;r.subnet='172.19.0.0/16';r.port=18080;r.args=types.SimpleNamespace(release_id='test');calls=[]
  r.run=lambda cmd,**kw:calls.append(cmd)
  r.network_gate(True);r.network_gate(True);r.network_gate(False)
  self.assertEqual(len(calls),2);self.assertIn('172.19.0.0/16',calls[0]);self.assertIn('NEW',calls[0]);self.assertIn('18080',calls[0]);self.assertIn('-D',calls[1])
 def test_startup_failure_preserves_background_writes(self):
  r=RollbackDecisionTests().fake('startup')
  with self.assertRaises(RuntimeError):r.execute()
  self.assertIn(('rollback',True),r.calls)

class ProbeIdentityTests(unittest.TestCase):
 def test_probe_identifies_itself_before_cloudflare_checks(self):
  from unittest.mock import patch, MagicMock
  from release_host import Release
  r=Release.__new__(Release);r.port=18080;r.env={'ADMIN_SECRET':'test-only'}
  response=MagicMock();response.status=200;response.headers={};response.read.return_value=b'{}';response.__enter__.return_value=response
  with patch('release_host.urllib.request.urlopen',return_value=response) as opened:
   r.request('/health',public=True)
   req=opened.call_args.args[0]
   self.assertEqual(req.get_header('User-agent'),'Codex2API-ReleaseCheck/1.0')
   self.assertIsNone(req.get_header('X-admin-key'))
if __name__=='__main__':unittest.main()

class DrainTests(unittest.TestCase):
 def make(self):
  from release_host import Release
  r=Release.__new__(Release);r.report={};r.event=lambda x:None
  return r
 def test_finishes_early_when_no_active_requests(self):
  from unittest.mock import patch
  r=self.make();r.request=lambda *a,**kw:(200,{},b'{"accounts":{"active_requests":0}}')
  with patch('release_host.time.sleep') as sleep:r.drain()
  sleep.assert_not_called();self.assertEqual(r.report['drain_remaining_requests'],0);self.assertFalse(r.report['drain_deadline_reached'])
 def test_missing_counter_waits_until_deadline(self):
  from unittest.mock import patch
  r=self.make();r.request=lambda *a,**kw:(200,{},b'{"accounts":{}}')
  with patch('release_host.time.monotonic',side_effect=[0,0,0,301,301,301]),patch('release_host.time.sleep') as sleep:r.drain()
  sleep.assert_not_called();self.assertIsNone(r.report['drain_remaining_requests']);self.assertTrue(r.report['drain_deadline_reached'])

class PublicRollbackDrainTests(unittest.TestCase):
 def test_public_failure_drains_before_stopping_new_app(self):
  import time
  from release_host import Release
  r=Release.__new__(Release);r.stopped=True;r.migrated=True;r.opened=True;r.maintenance=False;r.report={};r.rollback_compose='pinned';calls=[]
  r.event=lambda name:None;r.stop_migrator=lambda:None;r.network_gate=lambda on:None;r.caddy=lambda:{'apps':{'http':{'servers':{'s':{'routes':[{'match':[{'host':['codex.xingqiaolab.top']}],'handle':[]}]}}}}}
  r.load_caddy=lambda c:None;r.run=lambda *a,**k:b'exists';r.inspect=lambda n:{'State':{'Running':True}};r.ready=lambda:None;r.restore_route=lambda:None;r.write_compose=lambda c:None
  def drain():calls.append('drain');r.drain_started=time.monotonic()
  r.drain=drain;r.dc=lambda *a,**k:calls.append(a[0]);r.rollback(preserve_database=True)
  self.assertLess(calls.index('drain'),calls.index('stop'));self.assertFalse(r.report['database_restored'])
