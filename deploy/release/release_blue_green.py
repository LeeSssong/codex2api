#!/usr/bin/env python3
"""Codex-only compatible-schema release. Keep the old app until drain completes."""
import argparse,copy,fcntl,json,os,pathlib,re,stat,time,types
from release_host import Release,assert_codex_only_config,check_source,codex_route,replace_image,atomic_restore

def change_upstream(config,old_port,new_port):
 result=copy.deepcopy(config); count=0
 def visit(value):
  nonlocal count
  if isinstance(value,dict):
   if value.get('handler')=='reverse_proxy':
    for item in value.get('upstreams',[]):
     host,sep,port=item['dial'].rpartition(':')
     if not sep or int(port)!=old_port:raise ValueError('unexpected Codex upstream')
     item['dial']=host+':'+str(new_port);count+=1
   for item in value.values():visit(item)
  elif isinstance(value,list):
   for item in value:visit(item)
 visit(codex_route(result)['handle'])
 if count!=1:raise ValueError('expected one Codex upstream')
 assert_codex_only_config(config,result);return result

def change_caddyfile_upstream(text,old_port,new_port):
 """Change the single reverse_proxy endpoint in the Codex site block.

 The running Caddy container bind-mounts this file, so callers must write the
 returned bytes in place.  We intentionally reject ambiguous Caddyfiles rather
 than changing a similarly named Sub2API route.
 """
 site=re.search(r'(?m)^\s*(?P<name>(?:[^\s{]+\s*,\s*)*codex\.xingqiaolab\.top(?:\s*,[^\s{]+)*)\s*\{',text)
 if not site: raise ValueError('Codex site block not found')
 start=site.end(); depth=1; pos=start
 while depth and pos<len(text):
  if text[pos]=='{': depth+=1
  elif text[pos]=='}': depth-=1
  pos+=1
 if depth: raise ValueError('unterminated Codex site block')
 block=text[start:pos-1]
 matches=list(re.finditer(r'(?m)^\s*reverse_proxy\s+([^\n#]+)',block))
 if len(matches)!=1: raise ValueError('expected one Codex reverse_proxy directive')
 line=matches[0].group(1)
 endpoints=re.findall(r'(?<![\w.-])([^\s:]+):(\d+)(?!\d)',line)
 if len(endpoints)!=1 or int(endpoints[0][1])!=old_port:
  raise ValueError('unexpected Codex Caddy upstream')
 old_token=endpoints[0][0]+':'+endpoints[0][1]
 new_token=endpoints[0][0]+':'+str(new_port)
 changed=block.replace(old_token,new_token,1)
 return text[:start]+changed+text[pos-1:]

def caddyfile_bind_source(mounts,container_path='/etc/caddy/Caddyfile'):
 """Resolve a regular-file Caddy bind mount without touching container layers."""
 target=pathlib.PurePosixPath(container_path)
 candidates=[]
 for mount in mounts:
  if mount.get('Type')!='bind': continue
  destination=pathlib.PurePosixPath(mount.get('Destination',''))
  source=pathlib.Path(mount.get('Source',''))
  if destination==target:
   candidates.append(source)
  elif target.is_relative_to(destination):
   candidates.append(source/pathlib.PurePosixPath(*target.relative_to(destination).parts))
 if len(candidates)!=1: raise ValueError('expected one Caddyfile bind source')
 path=candidates[0]
 if not path.is_absolute() or '..' in path.parts or path.is_symlink(): raise ValueError('unsafe Caddyfile bind source')
 if path.exists() and not stat.S_ISREG(path.stat().st_mode): raise ValueError('Caddyfile bind source is not a regular file')
 return path

def persist_caddyfile(path,old_port,new_port,backup):
 """Backup and update a bind-mounted Caddyfile in place, preserving its inode."""
 path=pathlib.Path(path); data=path.read_bytes(); pathlib.Path(backup).write_bytes(data)
 updated=change_caddyfile_upstream(data.decode(),old_port,new_port).encode()
 with path.open('wb') as out:
  out.write(updated);out.flush();os.fsync(out.fileno())
 return data

class BlueGreenRelease(Release):
 def execute(self):
  # Deliberately no schema migration or credential-runtime update on this path.
  self.protected_before=self.protected_containers()
  check_source(self.inspect(self.args.image)['Config'].get('Labels',{}),self.args.revision,self.args.tree)
  if self.inspect(self.args.image)['Id']!=self.args.digest:raise ValueError('image digest mismatch')
  self.route_config=self.caddy();self.route_before=copy.deepcopy(codex_route(self.route_config)['handle'])
  caddy_mounts=self.inspect('sub2api-caddy-1').get('Mounts',[])
  self.caddyfile=caddyfile_bind_source(caddy_mounts)
  self.caddyfile_backup=self.dir/'Caddyfile.before'
  self.caddyfile_before=self.caddyfile.read_bytes()
  self.caddyfile_backup.write_bytes(self.caddyfile_before);self.caddyfile_backup.chmod(0o600)
  (self.dir/'caddy.before.json').write_text(json.dumps(self.route_config))
  oldport=int(self.old['HostConfig']['PortBindings'][str(self.port)+'/tcp'][0]['HostPort'])
  newport=oldport+1
  self.candidate='codex2api-candidate-'+self.args.release_id
  self.candidate_file=self.dir/'candidate.json'
  config=json.loads(self.dc('config','--format','json'))
  service=copy.deepcopy(config['services']['codex2api']);service['image']=self.args.image;service['container_name']=self.candidate
  service['ports']=[{'target':self.port,'published':str(newport),'protocol':'tcp','host_ip':'0.0.0.0'}]
  service.pop('depends_on',None)
  self.candidate_file.write_text(json.dumps({'services':{'codex2api':service},'networks':config.get('networks',{}),'volumes':config.get('volumes',{})}));self.candidate_file.chmod(0o600)
  self.switched=False
  try:
   self.run(['docker','compose','--project-name','codex2api-candidate','--project-directory',str(self.root),'-f',str(self.candidate_file),'up','-d','--no-deps','codex2api'])
   self.port=newport;self.ready()
   health=json.loads(self.request('/health')[2])
   if health.get('build_version')!='release-'+self.args.release_id:raise RuntimeError('candidate version mismatch')
   if self.inspect(self.candidate)['Image']!=self.args.digest:raise RuntimeError('candidate digest mismatch')
   overview=json.loads(self.request('/api/admin/credential-ops',True)[2])
   if not isinstance(overview.get('rules'),dict):raise RuntimeError('global rules unavailable')
   if any(not row['enabled'] for row in overview['accounts']):
    self.request_rules(overview['rules'])
   self.event('candidate-ready')
   # Update the bind source before Caddy's JSON config. In-place writes keep
   # the inode visible through a single-file bind mount after later reloads.
   persist_caddyfile(self.caddyfile,oldport,newport,self.caddyfile_backup)
   self.load_caddy(change_upstream(self.route_config,oldport,newport));self.switched=True
   self.event('traffic-switched')
   health=json.loads(self.request('/health',public=True)[2])
   if health.get('build_version')!='release-'+self.args.release_id:raise RuntimeError('public version mismatch')
   self.request('/api/admin/credential-ops',True,public=True)
   if b'<html' not in self.request('/admin/smart-ops/credentials',public=True)[2].lower():raise RuntimeError('admin shell missing')
   for name,expected in self.protected_before.items():
    actual=self.inspect(name)
    if actual['Id']!=expected['id'] or actual['State']['StartedAt']!=expected['started_at']:raise RuntimeError('unrelated container changed: '+name)
   self.port=oldport;self.drain()
   self.run(['docker','stop','-t','1','codex2api'])
   self.run(['docker','rename','codex2api','codex2api-rollback-'+self.args.release_id])
   self.run(['docker','rename',self.candidate,'codex2api'])
   # Worker DNS switches only after old requests have drained; shared task leases
   # protect processing across both app instances while they coexist.
   for network in self.old['NetworkSettings']['Networks']:
    self.run(['docker','network','disconnect',network,'codex2api-rollback-'+self.args.release_id])
   final=json.loads(self.dc('config','--format','json'));final['services']['codex2api']['image']=self.args.image
   for binding in final['services']['codex2api'].get('ports',[]):
    if int(binding['target'])==int(self.env.get('CODEX_PORT','18080')):binding['published']=str(newport)
   self.write_compose(json.dumps(final,indent=2)+'\n')
   for name,expected in self.protected_before.items():
    actual=self.inspect(name)
    if actual['Id']!=expected['id'] or actual['State']['StartedAt']!=expected['started_at']:raise RuntimeError('unrelated container changed: '+name)
   self.port=newport;self.request('/health');self.report['port']=newport;self.report['result']='success';self.report['rolled_back']=False
   self.event('public-feature-smoke-passed')
  except BaseException:
   rollback_name='codex2api-rollback-'+self.args.release_id
   if self.run(['docker','ps','-a','--filter','name=^/'+rollback_name+'$','--format','{{.Names}}']).strip():
    if self.run(['docker','ps','-a','--filter','name=^/codex2api$','--format','{{.Names}}']).strip():self.run(['docker','rename','codex2api',self.candidate])
    self.run(['docker','rename',rollback_name,'codex2api'])
    current=self.inspect('codex2api')
    for network in self.old['NetworkSettings']['Networks']:
     if network not in current['NetworkSettings']['Networks']:self.run(['docker','network','connect','--alias','codex2api',network,'codex2api'])
    self.run(['docker','start','codex2api'])
   if self.switched:
    restored=self.caddy();codex_route(restored)['handle']=self.route_before;self.load_caddy(restored)
   if hasattr(self,'caddyfile') and self.caddyfile_backup.exists():
    data=self.caddyfile_backup.read_bytes()
    with self.caddyfile.open('wb') as out:out.write(data);out.flush();os.fsync(out.fileno())
   self.write_compose(self.before)
   if self.run(['docker','ps','-a','--filter','name=^/'+self.candidate+'$','--format','{{.Names}}']).strip():self.run(['docker','rm','--force',self.candidate])
   self.report['result']='failed';self.report['rolled_back']=True;self.save()
   raise
 def request_rules(self,rules):
  import urllib.request
  req=urllib.request.Request('http://127.0.0.1:'+str(self.port)+'/api/admin/credential-ops/rules',json.dumps(rules).encode(),headers={'Content-Type':'application/json','X-Admin-Key':self.env['ADMIN_SECRET']},method='PUT')
  with urllib.request.urlopen(req,timeout=30) as response:response.read()

if __name__=='__main__':
 p=argparse.ArgumentParser()
 for name in ['image','digest','revision','tree','release-id']:p.add_argument('--'+name,required=True)
 args=p.parse_args();os.umask(0o077)
 with open('/var/lock/codex2api-release.lock','w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB);BlueGreenRelease(args).execute()
