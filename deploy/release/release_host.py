#!/usr/bin/env python3
"""Single-instance Codex2API schema release; run as root on the production host.
Only the codex app is stopped/recreated. PostgreSQL, Redis and Sub stay running.
An image built from verified/pushed clean main is mandatory. No credentials print.
"""
import argparse, copy, fcntl, ipaddress, json, os, pathlib, re, shutil, subprocess, time, urllib.request, urllib.error, stat, tempfile, types

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

def checked_file(path, private=False):
 path=pathlib.Path(path)
 if not path.is_absolute() or '..' in path.parts:raise ValueError('absolute canonical file path required')
 for parent in [*reversed(path.parents),path]:
  if parent.is_symlink():raise ValueError('symlink paths are not allowed')
 info=path.stat()
 if not stat.S_ISREG(info.st_mode):raise ValueError('regular file required')
 if private and (stat.S_IMODE(info.st_mode)&0o077 or info.st_uid!=os.geteuid()):raise ValueError('private owner-only file required')
 return path,info

def atomic_restore(path,data,mode,uid,gid):
 path=pathlib.Path(path);checked_file(path)
 fd,name=tempfile.mkstemp(prefix='.release-restore-',dir=path.parent)
 try:
  with os.fdopen(fd,'wb') as out:out.write(data);out.flush();os.fsync(out.fileno())
  os.chown(name,uid,gid);os.chmod(name,mode);os.replace(name,path)
 finally:
  if os.path.exists(name):os.unlink(name)

def host_asset_path(raw,mounts,ordinary=False):
 path=pathlib.PurePosixPath(raw)
 if not path.is_absolute() or '..' in path.parts or raw.startswith('s3:'):raise ValueError('unsupported BPS asset path')
 choices=[]
 for mount in mounts:
  dest=pathlib.PurePosixPath(mount['Destination'])
  if path==dest or dest in path.parents:choices.append((len(dest.parts),mount,dest))
 if not choices:raise ValueError('BPS asset is outside mounted storage')
 _,mount,dest=max(choices,key=lambda item:item[0])
 if mount['Type']!='bind':raise ValueError('BPS storage must be a bind mount')
 source=pathlib.Path(mount['Source']);result=source.joinpath(*path.relative_to(dest).parts)
 if not source.is_absolute() or '..' in source.parts:raise ValueError('invalid bind source')
 for component in [*reversed(result.parents),result]:
  try:info=component.lstat()
  except FileNotFoundError:continue
  if stat.S_ISLNK(info.st_mode) and not ordinary:raise ValueError('symlink BPS asset path')
 try:
  info=result.stat()
  if not ordinary and (not stat.S_ISREG(info.st_mode) or info.st_nlink!=1):raise ValueError('BPS asset is not an unshared regular file')
 except FileNotFoundError:pass
 return result.resolve() if ordinary else result

def quarantine_plan(assets,ordinary,mounts,destination):
 # Validate every object before moving any object or touching database rows.
 ordinary_paths=set(ordinary);seen=set();plan=[]
 for asset in assets:
  path=host_asset_path(asset['storage_path'],mounts)
  if asset['storage_path'] in ordinary_paths:raise ValueError('BPS file shared with ordinary image')
  if path in seen:raise ValueError('duplicate BPS storage path')
  seen.add(path)
  for raw in ordinary:
   try:other=host_asset_path(raw,mounts,ordinary=True)
   except ValueError:continue
   if path==other:raise ValueError('BPS file shared with ordinary image')
  if path.exists() and path.stat().st_dev!=destination.stat().st_dev:raise ValueError('quarantine must share the asset filesystem')
  target=destination/(str(int(asset['id']))+'.asset')
  if target.is_symlink():raise ValueError('symlink quarantine target')
  if target.exists():
   checked_file(target,private=True)
   if path.exists():raise RuntimeError('quarantine collision requires operator review')
  plan.append((asset,path,target))
 return plan

class Release:
 def __init__(self,args):
  self.args=args; self.root=pathlib.Path('/opt/codex2api'); self.compose=self.root/'docker-compose.yml'
  self.dir=self.root/'backups'/args.release_id; self.dir.mkdir(mode=0o700,parents=True,exist_ok=False)
  self.events=[]; self.started=time.monotonic(); self.maintenance=False; self.stopped=False; self.migrated=False; self.opened=False; self.app_started=False; self.gated=False
  self.bps=bool(getattr(args,'bps_release',False)); self.new_compose=None
  self.report={'root':str(self.root),'compose':str(self.compose),'bps_release':self.bps,'revision':args.revision,'tree':args.tree,'image':args.image,'digest':args.digest,'release_id':args.release_id,'events':self.events}
  self.before=self.compose.read_text(); (self.dir/'compose.before.yml').write_text(self.before)
  self.old=self.inspect('codex2api'); self.rollback_compose=image_pinned_compose(self.before,self.old); (self.dir/'compose.rollback.yml').write_text(self.rollback_compose); self.env=dict(v.split('=',1) for v in self.old['Config']['Env']); self.port=int(self.env.get('CODEX_PORT','18080'))
  if self.bps:
   (self.dir/'old-container.json').write_text(json.dumps(self.old));(self.dir/'old-container.json').chmod(0o600)
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
  temp=self.compose.with_suffix('.release-tmp');temp.write_text(text);shutil.copymode(self.compose,temp)
  if getattr(self,'bps',False):temp.chmod(0o600)
  os.replace(temp,self.compose)
 def request(self,path,auth=False,public=False,timeout=10):
  base='https://codex.xingqiaolab.top' if public else 'http://127.0.0.1:'+str(self.port)
  headers={"User-Agent":"Codex2API-ReleaseCheck/1.0"}
  if auth:
   secret=self.env.get('ADMIN_SECRET','')
   if not secret:raise RuntimeError('environment-backed admin credential required for release smoke')
   headers['X-Admin-Key']=secret
  with urllib.request.urlopen(urllib.request.Request(base+path,headers=headers),timeout=timeout) as response:
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
    value=json.loads(self.request('/api/admin/runtime-status',True,timeout=min(10,max(0.1,deadline-time.monotonic())))[2])['accounts']['active_requests']
    if not isinstance(value,int) or value<0:raise ValueError('invalid drain counter')
    remaining=value
    if remaining==0:break
   except Exception:
    # Missing telemetry is not proof of an empty pool. Preserve the full window.
    remaining=None
   remaining_seconds=deadline-time.monotonic()
   if remaining_seconds>0:time.sleep(min(2,remaining_seconds))
  self.report['drain_remaining_requests']=remaining
  self.report['drain_deadline_reached']=time.monotonic()>=deadline
  self.event('old-requests-drained')
 def feature_smoke(self,public=False):
  endpoints=['/api/admin/account-ops/module','/api/admin/account-ops/config','/api/admin/quality-ops/plans','/api/admin/quality-ops/history','/api/admin/account-ops/alerts','/api/admin/account-ops/token-guard/status','/api/admin/account-ops/token-guard/config','/api/admin/account-ops/token-guard/events?limit=1']
  if getattr(self,'bps',False):endpoints.append('/api/admin/settings/basispoints')
  for endpoint in endpoints:
   status,headers,payload=self.request(endpoint,True,public)
   if status!=200 or 'application/json' not in headers.get('Content-Type',headers.get('content-type','')):raise RuntimeError('feature endpoint did not return JSON: '+endpoint)
   json.loads(payload)
  if getattr(self,'bps',False):
   bps=json.loads(self.request('/api/admin/settings/basispoints',True,public)[2]);canonical=json.loads(self.request('/api/admin/settings',True,public)[2])
   if not isinstance(bps.get('enabled'),bool) or not isinstance(canonical.get('codex_basispoints_enabled'),bool) or bps.get('enabled') is not canonical.get('codex_basispoints_enabled'):raise RuntimeError('BPS enabled state differs from canonical settings')
   runtime=bps.get('image_relay_runtime',{})
   if runtime.get('source') not in ('environment','persisted') or runtime.get('validation_status') not in ('disabled','configuration_incomplete','configured_unverified'):raise RuntimeError('BPS settings diagnostics missing')
   if self.report.get('bps_signing_env') and runtime.get('signing_key_configured') is not True:raise RuntimeError('BPS signing env was not loaded')
   if runtime.get('backend')!='local':raise RuntimeError('BPS release requires local image backend')
  info=json.loads(self.request('/api/admin/system/build',True,public)[2])
  if info.get('source_revision')!=self.args.revision or info.get('source_tree')!=self.args.tree or info.get('mode')!='source_image' or info.get('supported') is not False:raise RuntimeError('managed build provenance mismatch')
  self.report['upstream_revision']=info.get('upstream_revision')
  self.report['upstream_check_status']='source-ancestry-verified-before-build'
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
  self.protected_before=self.protected_containers();self.report['protected_before']=self.protected_before;self.save()
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
  if self.bps:self.prepare_bps_release()
  snapshot=self.dir/'preflight.dump';self.dump(snapshot)
  scratch='codex_release_verify_'+re.sub('[^a-z0-9]','',self.args.release_id.lower())[-24:]
  self.run(['docker','exec','codex2api-postgres','sh','-c','exec createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$1"','sh',scratch])
  try:
   self.run(['docker','exec','-i','codex2api-postgres','sh','-c','exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$1"','sh',scratch],snapshot.read_bytes())
   self.sql('SELECT count(*) FROM accounts;',scratch)
   rehearsal_env=self.dir/'rehearsal.env'
   if any('\n' in value or '\r' in value for value in self.env.values()):raise ValueError('multiline environment requires explicit rehearsal serialization')
   rehearsal_env.write_text('\n'.join(key+'='+value for key,value in self.env.items())+'\n');rehearsal_env.chmod(0o600)
   rehearsal_name='codex2api-rehearsal-'+self.args.release_id
   try:
    self.run(['docker','run','--rm','--name',rehearsal_name,'--network','codex2api-net','--env-file',str(rehearsal_env),'-e','DATABASE_NAME='+scratch,'-e','CODEX_MIGRATE_ONLY=1',self.args.image],timeout=180)
    if self.bps:self.verify_bps_schema(scratch)
    if self.sql("SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='accounts' AND column_name='control_revision';",scratch)!='1':raise RuntimeError('rehearsal did not install account revision fence')
   finally:
    if self.run(['docker','ps','-aq','--filter','name=^'+rehearsal_name+'$']).strip():self.run(['docker','rm','-f',rehearsal_name],timeout=60)
    rehearsal_env.unlink(missing_ok=True)
  finally:self.run(['docker','exec','codex2api-postgres','sh','-c','exec dropdb -U "$POSTGRES_USER" "$1"','sh',scratch])
  self.event('preflight-and-restore-rehearsal-passed')
 def verify_bps_schema(self,db):
  expected={'system_settings':['basispoints_config','basispoints_image_relay_epoch'], 'account_codex_paths':['model_scope','models_json','auto_disable_on_403','cache_creation_as_input','policy_revision','disabled_by','disabled_reason','disabled_at'], 'image_assets':['relay_expires_at','relay_scope_hash','relay_epoch','relay_digest','relay_state','relay_request','relay_cleanup_next_attempt'],'image_relay_lock':['revision','rejections','cleanup_errors'],'image_relay_reservations':['token','bytes','assets','expires_at']}
  for table,columns in expected.items():
   values=','.join("'"+column+"'" for column in columns)
   count=self.sql("SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='"+table+"' AND column_name IN ("+values+");",db)
   if count!=str(len(columns)):raise RuntimeError('BPS migration fields missing: '+table)
 def bps_assets(self):
  return json.loads(self.sql("SELECT COALESCE(json_agg(row_to_json(a))::text,'[]') FROM image_assets a WHERE model='bps-inbound';"))
 def prepare_bps_release(self):
  if any(self.env.get(key,'').strip() for key in ['IMAGE_ASSET_PUBLIC_BASE_URL','BASISPOINTS_IMAGE_PUBLIC_ORIGIN']):raise RuntimeError('existing image origin requires a separately reviewed rollback plan')
  if any(str(a['storage_path']).startswith('s3:') for a in self.bps_assets()):raise RuntimeError('S3 BPS assets require a separately reviewed rollback plan')
  raw=self.dc('config','--format','json','--no-env-resolution');effective=json.loads(raw)
  paths={self.root/'.env'}
  for service in effective.get('services',{}).values():
   files=service.get('env_file',[])
   if isinstance(files,(str,dict)):files=[files]
   for item in files:
    rawpath=item.get('path') if isinstance(item,dict) else item
    if not rawpath:raise ValueError('unsupported env_file declaration')
    path=pathlib.Path(rawpath);paths.add(path if path.is_absolute() else self.root/path)
  snapshots=[]
  for i,path in enumerate(sorted(paths)):
   if not path.exists():
    if path==self.root/'.env':continue
    raise ValueError('configured env file is missing')
   path,info=checked_file(path)
   name='env-'+str(i)+'.snapshot';target=self.dir/name;target.write_bytes(path.read_bytes());target.chmod(0o600)
   snapshots.append({'path':str(path),'snapshot':name,'mode':stat.S_IMODE(info.st_mode),'uid':info.st_uid,'gid':info.st_gid})
  self.report['env_snapshots']=snapshots;self.save()
  signing=getattr(self.args,'bps_signing_env',None)
  if signing:
   signing,info=checked_file(signing,private=True)
   # This file must not override DB, routing or application credentials.
   values={}
   for line in signing.read_text().splitlines():
    if not line.strip() or line.lstrip().startswith('#'):continue
    key,sep,value=line.partition('=')
    if not sep or key!='IMAGE_ASSET_SIGNING_SECRET' or key in values or not value.strip():raise ValueError('signing env must contain only one nonempty IMAGE_ASSET_SIGNING_SECRET')
    values[key]=value
   if not values:raise ValueError('signing env is empty')
   app=effective['services']['codex2api']
   if 'IMAGE_ASSET_SIGNING_SECRET' in app.get('environment',{}):raise ValueError('compose environment overrides signing env')
   files=app.get('env_file',[])
   if isinstance(files,(str,dict)):files=[files]
   app['env_file']=[*files,{'path':str(signing),'required':True}]
   app['image']=self.args.image;self.new_compose=json.dumps(effective,indent=2)+'\n'
   self.report['bps_signing_env']=str(signing);self.save()
 def restore_env_snapshots(self):
  pending=[]
  for item in self.report.get('env_snapshots',[]):
   snapshot,info=checked_file(self.dir/item['snapshot'],private=True)
   target,_=checked_file(item['path'])
   pending.append((target,snapshot.read_bytes(),item))
  for target,data,item in pending:atomic_restore(target,data,item['mode'],item['uid'],item['gid'])
 def quarantine_bps_assets(self):
  # Called only after traffic is gated, migrator removed and app stopped.
  assets=self.bps_assets()
  ordinary=json.loads(self.sql("SELECT COALESCE(json_agg(storage_path)::text,'[]') FROM image_assets WHERE model IS DISTINCT FROM 'bps-inbound';"))
  directory=self.dir/'bps-quarantine';directory.mkdir(mode=0o700,exist_ok=True)
  if directory.is_symlink() or stat.S_IMODE(directory.stat().st_mode)&0o077:raise ValueError('unsafe quarantine directory')
  plan=quarantine_plan(assets,ordinary,self.old.get('Mounts',[]),directory)
  archive=directory/'metadata.json'
  if archive.exists():
   prior=json.loads(checked_file(archive,private=True)[0].read_text())
   prior_ids={int(a['id']):a for a in prior}
   for asset in assets:
    if int(asset['id']) in prior_ids and prior_ids[int(asset['id'])]!=asset:raise RuntimeError('quarantine metadata changed since previous attempt')
    prior_ids[int(asset['id'])]=asset
   assets_archive=list(prior_ids.values())
  else:assets_archive=assets
  # Archive durable metadata before moving objects. Never replace quarantined bytes.
  fd,name=tempfile.mkstemp(prefix='.metadata-',dir=directory)
  with os.fdopen(fd,'w') as out:json.dump(assets_archive,out);out.flush();os.fsync(out.fileno())
  os.chmod(name,0o600);os.replace(name,archive)
  for asset,path,target in plan:
   if target.exists():
    checked_file(target,private=True)
    if path.exists():raise RuntimeError('quarantine collision requires operator review')
    continue
   try:
    # Recheck against path changes before the atomic move.
    source=host_asset_path(asset['storage_path'],self.old.get('Mounts',[]))
    os.rename(source,target);os.chown(target,os.geteuid(),os.getegid());target.chmod(0o600)
    for parent in {source.parent,directory}:
     fd=os.open(parent,os.O_RDONLY)
     try:os.fsync(fd)
     finally:os.close(fd)
   except FileNotFoundError:
    if path.exists():raise
  if assets:
   ids=','.join(str(int(a['id'])) for a in assets)
   self.sql("BEGIN; DELETE FROM image_assets WHERE model='bps-inbound' AND id IN ("+ids+"); COMMIT;")
  if self.sql("SELECT count(*) FROM image_assets WHERE model='bps-inbound';")!='0':raise RuntimeError('BPS assets remain; old application restart blocked')
  self.report['bps_assets_quarantined']=len(assets_archive);self.save()
 @classmethod
 def from_existing(cls,args):
  directory=pathlib.Path(args.rollback_existing)
  if not directory.is_absolute():raise ValueError('absolute backup directory required')
  report_path,_=checked_file(directory/'release.json',private=True)
  report=json.loads(report_path.read_text())
  if not report.get('bps_release'):raise ValueError('manual BPS rollback requires a BPS release snapshot')
  release_id=report['release_id']
  if not re.fullmatch(r'[a-zA-Z0-9._-]+',release_id):raise ValueError('invalid saved release ID')
  r=cls.__new__(cls);r.args=types.SimpleNamespace(release_id=release_id);r.root=pathlib.Path(report['root']);r.compose=pathlib.Path(report['compose'])
  if r.root!=pathlib.Path('/opt/codex2api') or r.compose!=r.root/'docker-compose.yml':raise ValueError('unexpected saved release root')
  r.dir=pathlib.Path(tempfile.mkdtemp(prefix='rollback-attempt-',dir=directory));r.report=report;r.bps=True
  r.report['original_release_backup']=str(directory);r.report['events']=[]
  for item in report.get('env_snapshots',[]):
   source=checked_file(directory/item['snapshot'],private=True)[0];target=r.dir/item['snapshot'];target.write_bytes(source.read_bytes());target.chmod(0o600)
  r.events=report.setdefault('events',[]);r.started=time.monotonic();r.maintenance=False;r.stopped=True;r.migrated=True;r.opened=True;r.app_started=True;r.gated=False
  r.old=json.loads(checked_file(directory/'old-container.json',private=True)[0].read_text())
  r.rollback_compose=checked_file(directory/'compose.rollback.yml',private=True)[0].read_text()
  r.route_before=json.loads(checked_file(directory/'caddy-handle.before.json',private=True)[0].read_text())
  current=r.inspect('codex2api');r.env=dict(value.split('=',1) for value in current['Config']['Env']);r.port=int(r.env.get('CODEX_PORT','18080'))
  # Refuse an unrelated deployment; never roll back a later release by accident.
  if current['Image'] not in {report['digest'],r.old['Image']}:raise RuntimeError('current image does not belong to this release')
  r.run(['docker','image','inspect',r.old['Image']])
  network=json.loads(r.run(['docker','network','inspect','codex2api-net']))[0]
  subnets=[item['Subnet'] for item in network['IPAM']['Config'] if ipaddress.ip_network(item['Subnet']).version==4]
  if len(subnets)!=1:raise ValueError('expected one Codex IPv4 subnet')
  r.subnet=subnets[0];r.report['original_protected_before']=report.get('protected_before',{});r.protected_before=r.protected_containers();r.report['protected_before']=r.protected_before
  r.report['protected_changes_since_release']=[name for name,identity in r.report['original_protected_before'].items() if r.protected_before.get(name)!=identity];r.save()
  # An interrupted rollback may have already installed this precise gate.
  probe=['iptables','-w','10','-C','DOCKER-USER','-d',r.subnet,'-p','tcp','--dport',str(r.port),'-m','conntrack','--ctstate','NEW','-m','comment','--comment','codex-release-'+release_id,'-j','REJECT']
  result=subprocess.run(probe,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  if result.returncode not in (0,1):raise RuntimeError('cannot inspect saved rollback gate')
  r.gated=result.returncode==0
  return r
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
  if exists and self.inspect('codex2api')['State']['Running']:
   stop_grace=300
   if preserve_database and self.opened:
    self.drain();stop_grace=max(0,int(300-(time.monotonic()-self.drain_started)))
   self.dc('stop','-t',str(stop_grace),'codex2api',timeout=stop_grace+30)
  if getattr(self,'bps',False):self.restore_env_snapshots()
  self.write_compose(self.rollback_compose)
  if self.migrated and not preserve_database:
   self.run(['docker','exec','codex2api-postgres','sh','-c','dropdb --force -U "$POSTGRES_USER" "$POSTGRES_DB" && createdb -U "$POSTGRES_USER" -O "$POSTGRES_USER" "$POSTGRES_DB"'])
   self.run(['docker','exec','-i','codex2api-postgres','sh','-c','exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],(self.dir/'stopped.dump').read_bytes())
  if getattr(self,'bps',False):self.quarantine_bps_assets()
  self.dc('up','-d','--no-deps','codex2api');self.ready()
  self.verify_protected_containers() if getattr(self,'bps',False) else None
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
   self.write_compose(getattr(self,'new_compose',None) or replace_image(self.before,self.args.image));self.dc('config','--quiet')
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
   for path in ['/admin/smart-ops/quality','/admin/smart-ops/alerts','/admin/smart-ops/tokens']:
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
 for name in ['image','digest','revision','tree','release-id']:parser.add_argument('--'+name)
 parser.add_argument('--bps-release',action='store_true');parser.add_argument('--bps-signing-env');parser.add_argument('--rollback-existing')
 args=parser.parse_args()
 if args.rollback_existing:
  if args.bps_signing_env or any(getattr(args,n) for n in ['image','digest','revision','tree','release_id']):parser.error('rollback-existing cannot be combined with new-release parameters')
 else:
  if not all(getattr(args,n) for n in ['image','digest','revision','tree','release_id']):parser.error('new release requires image/digest/revision/tree/release-id')
  if not re.fullmatch(r'[a-zA-Z0-9._-]+',args.release_id):parser.error('invalid release ID')
  if args.bps_signing_env and not args.bps_release:parser.error('signing env requires bps-release')
 os.umask(0o077)
 with open('/var/lock/codex2api-release.lock','w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  if args.rollback_existing:
   release=Release.from_existing(args);release.rollback(preserve_database=True);release.verify_protected_containers()
  else:Release(args).execute()
if __name__=='__main__':main()
