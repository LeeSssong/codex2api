#!/usr/bin/env python3
"""In-place release of the approved smart-ops retirement. No secret output."""
import argparse, datetime, fcntl, json, os, pathlib, subprocess, time, urllib.request, urllib.error
from retire_smart_ops import retire_compose

def capture(args):
    return subprocess.check_output(args, text=True)
def run(args, **kw):
    subprocess.run(args, check=True, **kw)
def inspect(name):
    return json.loads(capture(['docker','inspect',name]))[0]
def private(path, value):
    path.write_text(value); path.chmod(0o600)
def sql(query):
    return subprocess.check_output(['docker','exec','-i','codex2api-postgres','psql','-XAt','-v','ON_ERROR_STOP=1','-U','codex2api','-d','codex2api'],input=query,text=True).strip()
def retained():
    # Exact retained business configuration; account operational fields may retire owned effects.
    tables=['api_keys','system_settings','account_groups','account_group_members','proxies','account_codex_capabilities','account_model_cooldowns','codex_state_config','codex_ipv6_state_config','model_quality_config','ipv6_egress_config','intelligence_check_settings','quality_test_prompts']
    result={t:sql("SELECT md5(COALESCE(string_agg(x,',' ORDER BY x),'')) FROM (SELECT row_to_json(t)::text x FROM "+t+" t) q") for t in tables}
    result['accounts']=sql("SELECT md5(COALESCE(string_agg(x,',' ORDER BY x),'')) FROM (SELECT (to_jsonb(a)-'credentials'-'updated_at'-'control_revision' || jsonb_build_object('credentials',credentials-'smart_ops_load_factor'-'smart_ops_initial_revision'-'smart_ops_bps_defaults'))::text x FROM accounts a) q")
    result['routes']=sql("SELECT md5(COALESCE(string_agg(x,',' ORDER BY x),'')) FROM (SELECT (to_jsonb(p)-'quality_bps'-'quality_bps_disabled_at'-'quality_bps_recovery_at'-'quality_bps_owner_revision'-'quality_bps_recovery_epoch')::text x FROM account_codex_paths p) q")
    return result

def deploy(image_ref, revision, tree):
    os.umask(0o077)
    old=inspect('codex2api'); image=json.loads(capture(['docker','image','inspect',image_ref]))[0]
    labels=image['Config'].get('Labels',{})
    assert labels.get('org.opencontainers.image.revision')==revision and labels.get('io.xingqiao.source-tree')==tree
    assert old['Config']['Labels'].get('org.opencontainers.image.revision')=='9d18793064af63e9533ab753a4b738e8c5d07a03', 'Production changed'
    assert image['Architecture']=='amd64'
    oldlabels=old['Config']['Labels']; project=oldlabels['com.docker.compose.project']
    source=pathlib.Path(oldlabels['com.docker.compose.project.config_files'])
    assert source.suffix=='.json', 'This release expects the verified production JSON config'
    current_env=dict(v.split('=',1) for v in old['Config']['Env'])
    assert current_env.get('DATABASE_NAME')=='codex2api' and current_env.get('DATABASE_USER')=='codex2api', 'Database target changed'
    config=retire_compose(json.loads(source.read_text()),image['Id'])
    stamp=datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).strftime('%Y%m%d-%H%M%S')
    folder=pathlib.Path('/opt/codex2api/backups')/(stamp+'-retire-smart-ops-'+revision[:12]);folder.mkdir(mode=0o700)
    private(folder/'compose.before.json',source.read_text());private(folder/'compose.updated.json',json.dumps(config))
    private(folder/'container.before.json',json.dumps(old))
    rootcompose=pathlib.Path('/opt/codex2api/docker-compose.yml')
    private(folder/'root-compose.before',rootcompose.read_text())
    resolved=json.loads(capture(['docker','compose','-f',str(rootcompose),'config','--format','json']))
    rootupdated=retire_compose(resolved,image['Id'])
    ids=capture(['docker','ps','-aq']).split()
    protected={x['Name']:(x['Id'],x['State']['StartedAt']) for x in json.loads(capture(['docker','inspect',*ids])) if x['Name'] not in ['/codex2api','/codex2api-credential-runtime']}
    base=['docker','compose','-p',project,'-f',str(folder/'compose.updated.json')]
    run(base+['config','--quiet'])
    private(folder/'phase.json',json.dumps({'phase':'prepared'}))
    print('Retirement evidence:',folder,flush=True)
    run(['docker','stop','-t','60','codex2api-credential-runtime','codex2api'])
    private(folder/'phase.json',json.dumps({'phase':'writers_stopped'}))
    # Verified production has no active owned effects; require unchanged before deleting data.
    for table in ['account_token_guard_ownership','account_quality_recovery','smart_ops_concurrency']:
        assert sql('SELECT count(*) FROM '+table)=='0', 'Owned effects changed; review before migration'
    before=retained();private(folder/'retained.before.json',json.dumps(before))
    with (folder/'stopped.dump').open('wb') as f:run(['docker','exec','codex2api-postgres','pg_dump','-U','codex2api','-d','codex2api','-Fc'],stdout=f)
    with (folder/'stopped.dump').open('rb') as f:run(['docker','exec','-i','codex2api-postgres','pg_restore','--list'],stdin=f,stdout=subprocess.DEVNULL)
    private(folder/'phase.json',json.dumps({'phase':'migrating'}))
    with (folder/'migration.log').open('w') as f:run(base+['run','--rm','--no-deps','-e','CODEX_MIGRATE_ONLY=1','codex2api'],stdout=f,stderr=subprocess.STDOUT)
    after=retained();private(folder/'retained.after.json',json.dumps(after))
    assert before==after,'Retained configuration changed; keep stopped and investigate'
    assert sql("SELECT count(*) FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'smart_ops_%' OR tablename LIKE 'credential_ops_%' OR tablename LIKE 'account_token_guard_%' OR tablename IN ('plugin_settings','account_ops_settings','account_ops_alerts','account_quality_plans','account_quality_rounds','account_quality_recovery'))")=='0'
    private(folder/'phase.json',json.dumps({'phase':'starting_application'}))
    run(base+['up','-d','--no-deps','--force-recreate','codex2api'])
    for _ in range(60):
        current=inspect('codex2api')
        if current['State'].get('Health',{}).get('Status')=='healthy':break
        if current['State']['Status'] in ['exited','dead']:raise RuntimeError('new application exited')
        time.sleep(2)
    else:raise RuntimeError('health timeout')
    assert current['Image']==image['Id']
    env=dict(v.split('=',1) for v in current['Config']['Env'])
    assert not any(k.startswith('CODEX2API_CREDENTIAL_OPS_') for k in env)
    origin='http://127.0.0.1:'+str(config['services']['codex2api']['ports'][0]['published'])
    def request(path):
        req=urllib.request.Request(origin+path,headers={'X-Admin-Key':env['ADMIN_SECRET']})
        try:
            with urllib.request.urlopen(req,timeout=20) as r:return r.status,r.headers.get('Content-Type',''),r.read()
        except urllib.error.HTTPError as e:return e.code,'',b''
    for path in ['/api/admin/settings','/api/admin/model-quality','/api/admin/ipv6-egress','/api/admin/quality-tests','/api/admin/state-pool']:
        status,ctype,body=request(path);assert status==200 and 'json' in ctype,path
    for path in ['/api/admin/plugins','/api/admin/smart-ops','/api/admin/credential-ops','/api/admin/account-ops/config','/api/admin/quality-ops/plans','/api/internal/credential-ops/claim']:
        assert request(path)[0]==404,path
    run(['docker','rm','codex2api-credential-runtime'])
    private(rootcompose,json.dumps(rootupdated,indent=2))
    for name in ['credential-app.env','credential-worker.env']:
        path=pathlib.Path('/opt/codex2api/secrets')/name
        if path.exists():
            assert not path.is_symlink();path.unlink()
    for name,identity in protected.items():
        obj=inspect(name);assert (obj['Id'],obj['State']['StartedAt'])==identity,name
    private(folder/'release.json',json.dumps({'revision':revision,'tree':tree,'digest':image['Id'],'result':'success','retained_configuration_equal':True,'retired_tables_remaining':0,'worker_removed':True,'historical_backups_preserved':True},indent=2))
    private(folder/'phase.json',json.dumps({'phase':'completed'}))
    print('RETIREMENT VERIFIED',folder,flush=True)

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--image',required=True);parser.add_argument('--revision',required=True);parser.add_argument('--tree',required=True);a=parser.parse_args()
    with open('/var/lock/codex2api-release.lock','w') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        try:
            deploy(a.image,a.revision,a.tree)
        except BaseException:
            print('RETIREMENT INCOMPLETE: inspect the recorded phase and migration log; do not restore an old image against the retired schema.',flush=True)
            raise
