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

class BPSReleaseSafetyTests(unittest.TestCase):
 def fixture(self):
  import tempfile,pathlib,types
  from release_host import Release
  tmp=tempfile.TemporaryDirectory();self.addCleanup(tmp.cleanup);root=pathlib.Path(tmp.name).resolve()
  data=root/'data';data.mkdir();backup=root/'backup';backup.mkdir(mode=0o700)
  r=Release.__new__(Release);r.dir=backup;r.root=root;r.bps=True;r.report={};r.args=types.SimpleNamespace(image='new',bps_signing_env=None)
  r.old={'Mounts':[{'Type':'bind','Source':str(data),'Destination':'/data'}]};r.env={};r.save=lambda:None
  return r,data
 def test_quarantine_moves_only_bps_and_preserves_metadata_and_quota_until_move(self):
  import json,stat
  r,data=self.fixture();(data/'bps.png').write_bytes(b'secret');(data/'ordinary.png').write_bytes(b'keep')
  asset={'id':1,'storage_path':'/data/bps.png','model':'bps-inbound'};r.bps_assets=lambda:[asset];calls=[]
  def sql(q):
   calls.append(q)
   if 'json_agg(storage_path)' in q:return '["/data/ordinary.png"]'
   if q.startswith('BEGIN'):
    self.assertFalse((data/'bps.png').exists());self.assertEqual((r.dir/'bps-quarantine/1.asset').read_bytes(),b'secret');return ''
   return '0'
  r.sql=sql;r.quarantine_bps_assets()
  self.assertEqual((data/'ordinary.png').read_bytes(),b'keep');self.assertEqual(json.loads((r.dir/'bps-quarantine/metadata.json').read_text()),[asset])
  self.assertEqual(stat.S_IMODE((r.dir/'bps-quarantine/metadata.json').stat().st_mode),0o600)
  self.assertTrue(any("model='bps-inbound' AND id IN (1)" in q for q in calls))
 def test_all_paths_validated_before_any_file_move_or_row_delete(self):
  r,data=self.fixture();(data/'good').write_bytes(b'keep');r.bps_assets=lambda:[{'id':1,'storage_path':'/data/good'},{'id':2,'storage_path':'s3://bucket/key'}]
  calls=[];r.sql=lambda q:calls.append(q) or '[]'
  with self.assertRaises(ValueError):r.quarantine_bps_assets()
  self.assertTrue((data/'good').exists());self.assertFalse(any(q.startswith('BEGIN') for q in calls))
 def test_rejects_escape_symlink_unmounted_and_shared_ordinary_file(self):
  from release_host import host_asset_path,quarantine_plan
  r,data=self.fixture();(data/'asset').write_bytes(b'x');(data/'link').symlink_to(data/'asset');mounts=r.old['Mounts']
  for raw in ['/data/../private','/other/asset','/data/link']:
   with self.subTest(raw=raw),self.assertRaises(ValueError):host_asset_path(raw,mounts)
  for ordinary in [['/data/asset'],['/data/link']]:
   with self.assertRaises(ValueError):quarantine_plan([{'id':1,'storage_path':'/data/asset'}],ordinary,mounts,r.dir)
 def test_missing_file_is_allowed_but_permission_error_is_not(self):
  from release_host import host_asset_path
  from unittest.mock import patch
  r,data=self.fixture();self.assertEqual(host_asset_path('/data/missing',r.old['Mounts']),data/'missing')
  with patch('release_host.pathlib.Path.lstat',side_effect=PermissionError('denied')):
   with self.assertRaises(PermissionError):host_asset_path('/data/missing',r.old['Mounts'])
 def test_quarantine_failed_move_retains_database_rows(self):
  from unittest.mock import patch
  r,data=self.fixture();(data/'asset').write_bytes(b'x');r.bps_assets=lambda:[{'id':1,'storage_path':'/data/asset'}];calls=[];r.sql=lambda q:calls.append(q) or '[]'
  with patch('release_host.os.rename',side_effect=OSError('disk failure')):
   with self.assertRaises(OSError):r.quarantine_bps_assets()
  self.assertTrue((data/'asset').exists());self.assertFalse(any(q.startswith('BEGIN') for q in calls))
 def test_environment_snapshots_and_extra_signing_file_only_affect_new_compose(self):
  import json,stat
  r,data=self.fixture();old=r.root/'.env';old.write_text('ADMIN_SECRET=fixture\n');old.chmod(0o640)
  extra=r.root/'signing.env';extra.write_text('IMAGE_ASSET_SIGNING_SECRET=fixture-only\n');extra.chmod(0o600);r.args.bps_signing_env=str(extra)
  effective={'services':{'codex2api':{'image':'old','env_file':[{'path':str(old),'required':True}]},'postgres':{'image':'pg'}}}
  r.dc=lambda *a:json.dumps(effective).encode();r.bps_assets=lambda:[];r.prepare_bps_release()
  result=json.loads(r.new_compose);self.assertEqual(result['services']['codex2api']['env_file'][-1]['path'],str(extra));self.assertEqual(result['services']['postgres'],effective['services']['postgres'])
  self.assertEqual(old.read_text(),'ADMIN_SECRET=fixture\n');old.write_text('changed');r.restore_env_snapshots();self.assertEqual(old.read_text(),'ADMIN_SECRET=fixture\n');self.assertEqual(stat.S_IMODE(old.stat().st_mode),0o640)
 def test_existing_origin_and_s3_fail_closed(self):
  r,data=self.fixture();r.env={'IMAGE_ASSET_PUBLIC_BASE_URL':'https://images.example'}
  with self.assertRaises(RuntimeError):r.prepare_bps_release()
  r.env={};r.bps_assets=lambda:[{'storage_path':'s3://bucket/a'}]
  with self.assertRaises(RuntimeError):r.prepare_bps_release()
 def test_schema_assertion_includes_latest_retry_field(self):
  r,data=self.fixture();queries=[]
  def sql(q,db):
   queries.append(q);return str(len(q.split(' IN (')[1].split(')')[0].split(',')))
  r.sql=sql;r.verify_bps_schema('scratch');self.assertTrue(any('relay_cleanup_next_attempt' in q for q in queries));self.assertEqual(len(queries),5)
 def test_quarantine_failure_keeps_maintenance_and_never_starts_old_app(self):
  import time
  r=RollbackDecisionTests().fake(None);r.bps=True;r.stopped=True;r.migrated=True;r.opened=True;r.rollback_compose='old';r.event=lambda n:None;r.stop_migrator=lambda:None
  r.run=lambda *a,**k:b'current';r.inspect=lambda n:{'State':{'Running':True}};r.restore_env_snapshots=lambda:None
  def fail():raise RuntimeError('unsafe BPS path')
  r.quarantine_bps_assets=fail;calls=[];r.dc=lambda *a,**k:calls.append(a[0])
  from release_host import Release
  with self.assertRaises(RuntimeError):Release.rollback(r,preserve_database=True)
  self.assertTrue(r.maintenance);self.assertTrue(r.gated);self.assertNotIn('up',calls)
 def test_manual_restore_loads_saved_state_without_overwriting_original_backup(self):
  import json,types
  from unittest.mock import patch
  from release_host import Release
  r,data=self.fixture();directory=r.dir
  original={'root':'/opt/codex2api','compose':'/opt/codex2api/docker-compose.yml','bps_release':True,'release_id':'test','digest':'new','events':[{'stage':'success'}],'protected_before':{'sub':'previous'},'env_snapshots':[]}
  saved={'release.json':original,'old-container.json':{'Image':'old','Mounts':[]},'caddy-handle.before.json':[]}
  for name,value in saved.items():(directory/name).write_text(json.dumps(value));(directory/name).chmod(0o600)
  (directory/'compose.rollback.yml').write_text('old compose');(directory/'compose.rollback.yml').chmod(0o600)
  before=(directory/'release.json').read_bytes()
  def run(self,cmd,**kw):
   if cmd[:3]==['docker','network','inspect']:return b'[{"IPAM":{"Config":[{"Subnet":"172.19.0.0/16"}]}}]'
   return b'[]'
  with patch.object(Release,'inspect',return_value={'Image':'new','Config':{'Env':['CODEX_PORT=18080','ADMIN_SECRET=fixture']}}),patch.object(Release,'run',run),patch.object(Release,'protected_containers',return_value={'sub':'current'}),patch('release_host.subprocess.run',return_value=types.SimpleNamespace(returncode=1)):
   loaded=Release.from_existing(types.SimpleNamespace(rollback_existing=str(directory)))
  self.assertNotEqual(loaded.dir,directory);self.assertEqual((directory/'release.json').read_bytes(),before)
  self.assertEqual(loaded.rollback_compose,'old compose');self.assertEqual(loaded.protected_before,{'sub':'current'});self.assertEqual(loaded.report['protected_changes_since_release'],['sub'])
 def test_manual_restore_rejects_unrelated_current_image(self):
  import json,types
  from unittest.mock import patch
  from release_host import Release
  r,data=self.fixture();directory=r.dir
  for name,value in {'release.json':{'root':'/opt/codex2api','compose':'/opt/codex2api/docker-compose.yml','bps_release':True,'release_id':'test','digest':'new'},'old-container.json':{'Image':'old'},'caddy-handle.before.json':[]}.items():
   (directory/name).write_text(json.dumps(value));(directory/name).chmod(0o600)
  (directory/'compose.rollback.yml').write_text('old');(directory/'compose.rollback.yml').chmod(0o600)
  with patch.object(Release,'inspect',return_value={'Image':'other','Config':{'Env':[]}}),self.assertRaises(RuntimeError):Release.from_existing(types.SimpleNamespace(rollback_existing=str(directory)))
 def test_feature_smoke_checks_actual_bps_diagnostics_and_canonical_flag(self):
  import json,types
  r,data=self.fixture();r.args=types.SimpleNamespace(revision='rev',tree='tree');r.report={'bps_signing_env':'protected'}
  payload={'enabled':False,'image_relay_runtime':{'source':'environment','validation_status':'disabled','signing_key_configured':True,'backend':'local'}}
  def request(path,auth=False,public=False):
   value={}
   if path.endswith('/basispoints'):value=payload
   elif path.endswith('/settings'):value={'codex_basispoints_enabled':False}
   elif path.endswith('/build'):value={'source_revision':'rev','source_tree':'tree','mode':'source_image','supported':False}
   return 200,{'Content-Type':'application/json'},json.dumps(value).encode()
  r.request=request;r.feature_smoke();payload['image_relay_runtime']['source']='persisted';r.feature_smoke();self.assertEqual(r.report['upstream_check_status'],'source-ancestry-verified-before-build')
  payload['enabled']=True
  with self.assertRaises(RuntimeError):r.feature_smoke()
  payload['enabled']=False;payload['image_relay_runtime']['signing_key_configured']=False
  with self.assertRaises(RuntimeError):r.feature_smoke()
