#!/usr/bin/env python3
"""Single-instance Codex2API schema release; run as root on the production host.
Only the codex app is stopped/recreated. PostgreSQL, Redis and Sub stay running.
An image built from verified/pushed clean main is mandatory. No credentials print.
"""
import argparse, copy, fcntl, ipaddress, json, os, pathlib, re, shutil, subprocess, time, urllib.request, urllib.error

def codex_route(config):
 matches=[]
 for server in config.get('apps',{}).get('http',{}).get('servers',{}).values():
  for route in server.get('routes',[]):
   hosts=[h for m in route.get('match',[]) for h in m.get('host',[])]
   if 'codex.xingqiaolab.top' in hosts: matches.append(route)
 if len(matches)!=1: raise ValueError('expected exactly one Codex route')
 return matches[0]

def maintenance_config(config):
 result=copy.deepcopy(config)
 codex_route(result)['handle']=[{'handler':'static_response','status_code':503,'body':'Codex2API is being updated. Please retry shortly.','headers':{'Retry-After':['120'],'Content-Type':['text/plain; charset=utf-8']}}]
 return result

def replace_image(compose,image):
 pattern=r'(^  codex2api:\s*\n(?:(?!^  \S).)*?^    image:\s*)[^\n]+'
 result,count=re.subn(pattern,lambda m:m.group(1)+image,compose,count=1,flags=re.M|re.S)
 if count!=1: raise ValueError('cannot identify application image in compose')
 return result

def check_source(labels,revision,tree):
 if labels.get('org.opencontainers.image.revision')!=revision or labels.get('io.xingqiao.source-tree')!=tree:
  raise ValueError('image source labels do not match release manifest')

def module_state(api_value, stored_value):
 return api_value.get('enabled') is True if api_value is not None else stored_value == 'true'

def image_pinned_compose(compose, container):
 return replace_image(compose, container['Image'])

def assert_unchanged_containers(before, after):
 changed=[name for name,identity in before.items() if after.get(name)!=identity]
 if changed:raise RuntimeError('unrelated containers changed: '+','.join(changed))

class Release:
 def __init__(self,args):
  self.args=args; self.root=pathlib.Path('/opt/codex2api'); self.compose=self.root/'docker-compose.yml'
  self.dir=self.root/'backups'/args.release_id; self.dir.mkdir(mode=0o700,parents=True,exist_ok=False)
  self.events=[]; self.started=time.monotonic(); self.maintenance=False; self.stopped=False; self.migrated=False; self.opened=False; self.app_started=False; self.gated=False
  self.report={'revision':args.revision,'tree':args.tree,'image':args.image,'digest':args.digest,'release_id':args.release_id,'events':self.events}
  self.before=self.compose.read_text(); (self.dir/'compose.before.yml').write_text(self.before)
  self.old=self.inspect('codex2api'); self.rollback_compose=image_pinned_compose(self.before,self.old); (self.dir/'compose.rollback.yml').write_text(self.rollback_compose); self.env=dict(v.split('=',1) for v in self.old['Config']['Env']); self.port=int(self.env.get('CODEX_PORT','18080'))
  self.report['rollback_image_id']=self.old['Image']; self.report['rollback_compose']=str(self.dir/'compose.rollback.yml')
  self.save()
 def event(self,name):
  item={'stage':name,'elapsed_seconds':round(time.monotonic()-self.started,2)};self.events.append(item);self.save();print(json.dumps(item),flush=True)
 def save(self):
  target=self.dir/'release.json';target.write_text(json.dumps(self.report,indent=2));target.chmod(0o600)
 def run(self,cmd,data=None,timeout=180,output=None):
  with (self.dir/'commands.log').open('ab') as log:
   r=subprocess.run(cmd,input=data,stdout=output or subprocess.PIPE,stderr=log,timeout=timeout)
   if r.returncode: raise RuntimeError('command failed: '+cmd[0]+' (details in protected release log)')
   return r.stdout
 def inspect(self,name): return json.loads(self.run(['docker','inspect',name]))[0]
 def dc(self,*args,timeout=180):
  return self.run(['docker','compose','--project-directory',str(self.root),'-f',str(self.compose),*args],timeout=timeout)
 def sql(self,sql,db=None):
  cmd=['docker','exec','-i']
  if db:cmd+=['-e','PGDATABASE='+db]
  cmd+=['codex2api-postgres','sh','-c','exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "${PGDATABASE:-$POSTGRES_DB}" -At']
  return self.run(cmd,sql.encode()).decode().strip()
 def dump(self,path):
  with path.open('wb') as out:self.run(['docker','exec','codex2api-postgres','sh','-c','exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc'],output=out)
  self.run(['docker','exec','-i','codex2api-postgres','pg_restore','--list'],path.read_bytes())
 def caddy(self):return json.loads(self.run(['docker','exec','sub2api-caddy-1','wget','-qO-','http://127.0.0.1:2019/config/']))
 def load_caddy(self,config):
  self.run(['docker','exec','-i','sub2api-caddy-1','wget','-qO-','--header=Content-Type:application/json','--post-file=/dev/stdin','http://127.0.0.1:2019/load'],json.dumps(config).encode())
 def network_gate(self,enable):
  if self.gated==enable:return
  rule=["-d",self.subnet,"-p","tcp","--dport",str(self.port),"-m","conntrack","--ctstate","NEW","-m","comment","--comment","codex-release-"+self.args.release_id,"-j","REJECT"]
  command=["iptables","-w","10"]+(["-I","DOCKER-USER","1"] if enable else ["-D","DOCKER-USER"])+rule
  self.run(command,timeout=20);self.gated=enable
 def restore_route(self):
  current=self.caddy();codex_route(current)['handle']=self.route_before;self.load_caddy(current);self.maintenance=False
 def write_compose(self,text):
  temp=self.compose.with_suffix('.release-tmp');temp.write_text(text);shutil.copymode(self.compose,temp);os.replace(temp,self.compose)
 def request(self,path,auth=False,public=False):
  base='https://codex.xingqiaolab.top' if public else 'http://127.0.0.1:'+str(self.port)
  headers={"User-Agent":"Codex2API-ReleaseCheck/1.0"}
  if auth:
   secret=self.env.get('ADMIN_SECRET','')
   if not secret:raise RuntimeError('environment-backed admin credential required for release smoke')
   headers['X-Admin-Key']=secret
  with urllib.request.urlopen(urllib.request.Request(base+path,headers=headers),timeout=10) as response:
   payload=response.read();return response.status,dict(response.headers),payload
 def ready(self):
  deadline=time.monotonic()+90
  while time.monotonic()<deadline:
   try:
    status,headers,payload=self.request('/health')
    if status==200 and json.loads(payload).get('status')=='ok':return
   except Exception:pass
   time.sleep(1)
  raise RuntimeError('readiness deadline exceeded')
 def protected_containers(self):
  names=['codex2api-postgres','codex2api-redis','sub2api-sub2api-green-1','sub2api-sub2api-worker-1','sub2api-model-detector-1','sub2api-postgres-1','sub2api-redis-1','sub2api-caddy-1','sub2api-relay-ops-1']
  return {name:self.inspect(name)['Id'] for name in names}
 def verify_protected_containers(self):
  assert_unchanged_containers(self.protected_before,self.protected_containers())
 def drain(self):
  self.drain_started=time.monotonic();deadline=self.drain_started+300; remaining=None
  while time.monotonic()<deadline:
   try:
    value=json.loads(self.request('/api/admin/runtime-status',True)[2])['accounts']['active_requests']
    if not isinstance(value,int) or value<0:raise ValueError('invalid drain counter')
    remaining=value
    if remaining==0:break
   except Exception:
    # Missing telemetry is not proof of an empty pool. Preserve the full window.
    remaining=None
   time.sleep(2)
  self.report['drain_remaining_requests']=remaining
  self.report['drain_deadline_reached']=time.monotonic()>=deadline
  self.event('old-requests-drained')
 def feature_smoke(self,public=False):
  endpoints=['/api/admin/account-ops/module','/api/admin/account-ops/config','/api/admin/quality-ops/plans','/api/admin/quality-ops/history','/api/admin/account-ops/alerts','/api/admin/account-ops/token-guard/status','/api/admin/account-ops/token-guard/config','/api/admin/account-ops/token-guard/events?limit=1']
  for endpoint in endpoints:
   status,headers,payload=self.request(endpoint,True,public)
   if status!=200 or 'application/json' not in headers.get('Content-Type',headers.get('content-type','')):raise RuntimeError('feature endpoint did not return JSON: '+endpoint)
   json.loads(payload)
  info=json.loads(self.request('/api/admin/system/update',True,public)[2])
  if info.get('source_revision')!=self.args.revision or info.get('source_tree')!=self.args.tree or info.get('mode')!='source_image' or info.get('supported') is not False:raise RuntimeError('managed build provenance mismatch')
  self.report['upstream_revision']=info.get('upstream_revision')
  self.report['upstream_check_status']=info.get('check_status')
 def preflight(self):
  image=json.loads(self.run(['docker','image','inspect',self.args.image]))[0]
  if image['Id']!=self.args.digest:raise ValueError('image digest mismatch')
  check_source(image['Config'].get('Labels') or {},self.args.revision,self.args.tree)
  if image['Architecture']!='amd64':raise ValueError('expected amd64 image')
  self.run(['iptables','-S','DOCKER-USER'])
  network=json.loads(self.run(['docker','network','inspect','codex2api-net']))[0]
  subnets=[item['Subnet'] for item in network['IPAM']['Config'] if ipaddress.ip_network(item['Subnet']).version==4]
  if len(subnets)!=1:raise ValueError('expected one Codex IPv4 subnet')
  self.subnet=subnets[0]
  self.protected_before=self.protected_containers()
  self.dc('config','--quiet');self.request('/health');self.request('/api/admin/settings',True)
  self.request('/health',public=True);self.request('/api/admin/settings',True,public=True)
  self.original_settings=json.loads(self.request('/api/admin/settings',True)[2])
  try:module_info=json.loads(self.request('/api/admin/account-ops/module',True)[2])
  except urllib.error.HTTPError as err:
   if err.code!=404:raise
   module_info=None
  persisted=self.sql("SELECT value FROM account_ops_settings WHERE key='module_enabled';")
  self.original_account_ops_enabled=module_state(module_info,persisted)
  self.route_before=copy.deepcopy(codex_route(self.caddy())['handle'])
  (self.dir/'caddy-handle.before.json').write_text(json.dumps(self.route_before))
  snapshot=self.dir/'preflight.dump';self.dump(snapshot)
  scratch='codex_release_verify_'+re.sub('[^a-z0-9]','',self.args.release_id.lower())[-24:]
  self.run(['docker','exec','codex2api-postgres','sh','-c','exec createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$1"','sh',scratch])
  try:
   self.run(['docker','exec','-i','codex2api-postgres','sh','-c','exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$1"','sh',scratch],snapshot.read_bytes())
   self.sql('SELECT count(*) FROM accounts;',scratch)
  finally:self.run(['docker','exec','codex2api-postgres','sh','-c','exec dropdb -U "$POSTGRES_USER" "$1"','sh',scratch])
  self.event('preflight-and-restore-rehearsal-passed')
 def stop_migrator(self):
  name="codex2api-migrate-"+self.args.release_id
  query=["docker","ps","-aq","--filter","name=^"+name+"$"]
  if self.run(query).strip():self.run(["docker","rm","-f",name],timeout=60)
  if self.run(query).strip():raise RuntimeError("migration container still exists; database recovery blocked")
 def rollback(self,preserve_database=False):
  self.event('rollback-started')
  self.stop_migrator()
  if not self.stopped:return
  if preserve_database:
   self.network_gate(True)
   self.load_caddy(maintenance_config(self.caddy()));self.maintenance=True
  exists=self.run(['docker','ps','-aq','--filter','name=^codex2api$']).strip()
  if exists and self.inspect('codex2api')['State']['Running']:self.dc('stop','-t','300','codex2api',timeout=330)
  self.write_compose(self.rollback_compose)
  if self.migrated and not preserve_database:
   self.run(['docker','exec','codex2api-postgres','sh','-c','dropdb --force -U "$POSTGRES_USER" "$POSTGRES_DB" && createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$POSTGRES_DB"'])
   self.run(['docker','exec','-i','codex2api-postgres','sh','-c','exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],(self.dir/'stopped.dump').read_bytes())
  self.dc('up','-d','--no-deps','codex2api');self.ready()
  self.network_gate(False)
  if self.maintenance:self.restore_route()
  self.report['rolled_back']=True;self.report['database_restored']=self.migrated and not preserve_database;self.event('rollback-completed')
 def execute(self):
  self.preflight()
  try:
   self.network_gate(True)
   self.maintenance=True;self.load_caddy(maintenance_config(self.caddy()));self.event('maintenance-on')
   self.drain()
   self.stopped=True;stop_grace=max(0,int(300-(time.monotonic()-self.drain_started)));self.dc('stop','-t',str(stop_grace),'codex2api',timeout=stop_grace+30);self.event('app-stopped')
   self.dump(self.dir/'stopped.dump');self.event('consistent-backup-completed')
   self.write_compose(replace_image(self.before,self.args.image));self.dc('config','--quiet')
   self.migrated=True
   self.dc('run','--rm','--name','codex2api-migrate-'+self.args.release_id,'--no-deps','-e','CODEX_MIGRATE_ONLY=1','codex2api',timeout=180);self.event('migration-completed')
   self.app_started=True
   self.dc('up','-d','--no-deps','codex2api');self.ready()
   if self.inspect('codex2api')['Image']!=self.args.digest:raise RuntimeError('running image mismatch')
   self.feature_smoke()
   if (json.loads(self.request('/api/admin/account-ops/module',True)[2]).get('enabled') is True) != self.original_account_ops_enabled:raise RuntimeError('existing account-ops module state changed')
   settings=json.loads(self.request('/api/admin/settings',True)[2])
   for key in ['codex_basispoints_enabled']:
    if key in self.original_settings and settings.get(key)!=self.original_settings.get(key):raise RuntimeError('existing setting changed: '+key)
   for path in ['/admin/quality-ops','/admin/account-ops','/admin/token-guard']:
    if b'<html' not in self.request(path)[2].lower():raise RuntimeError('admin UI shell missing')
   self.verify_protected_containers()
   self.event('internal-feature-smoke-passed')
   self.opened=True;self.network_gate(False);self.restore_route();self.event('traffic-restored')
   self.request('/health',public=True)
   self.feature_smoke(public=True)
   self.verify_protected_containers()
   self.report['result']='success';self.report['rolled_back']=False;self.event('public-feature-smoke-passed')
  except BaseException:
   self.report['result']='failed';self.save()
   if not self.opened and not self.app_started:
    try:self.rollback()
    finally:
     if not self.stopped:
      try:
       if self.maintenance:self.restore_route()
      finally:self.network_gate(False)
   else:
    # Additive migration: restore old app/config only, preserving all resumed writes.
    self.rollback(preserve_database=True)
   raise

def main():
 parser=argparse.ArgumentParser()
 for name in ['image','digest','revision','tree','release-id']:parser.add_argument('--'+name,required=True)
 args=parser.parse_args()
 if not re.fullmatch(r'[a-zA-Z0-9._-]+',args.release_id):parser.error('invalid release ID')
 os.umask(0o077)
 with open('/var/lock/codex2api-release.lock','w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  Release(args).execute()
if __name__=='__main__':main()
