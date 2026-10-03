#!/usr/bin/env python3
"""Codex-only compatible-schema release. Keep the old app until drain completes."""
import argparse,copy,fcntl,json,os,pathlib,time,types
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

class BlueGreenRelease(Release):
 def execute(self):
  # Deliberately no schema migration or credential-runtime update on this path.
  self.protected_before=self.protected_containers()
  check_source(self.inspect(self.args.image)['Config'].get('Labels',{}),self.args.revision,self.args.tree)
  if self.inspect(self.args.image)['Id']!=self.args.digest:raise ValueError('image digest mismatch')
  self.route_config=self.caddy();self.route_before=copy.deepcopy(codex_route(self.route_config)['handle'])
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
