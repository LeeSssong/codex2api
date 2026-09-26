import argparse, base64, hashlib, hmac, json, os, pathlib, re, secrets, sqlite3, subprocess, time, urllib.request, urllib.error

p=argparse.ArgumentParser();p.add_argument('--manifest',required=True);p.add_argument('--workdir',required=True);a=p.parse_args()
os.umask(0o077)
manifest=json.loads(pathlib.Path(a.manifest).read_text())
root=pathlib.Path(a.workdir).resolve();root.mkdir(parents=True,exist_ok=False);data=root/'data';data.mkdir();(data/'images').mkdir()
admin=secrets.token_hex(24);signing=secrets.token_hex(32);env=root/'runtime.env'
env.write_text('\n'.join(['ADMIN_SECRET='+admin,'IMAGE_ASSET_SIGNING_SECRET='+signing,'CODEX_PORT=8080','DATABASE_DRIVER=sqlite','DATABASE_PATH=/data/smoke.db','CACHE_DRIVER=memory','GIN_MODE=release','IMAGE_ASSET_DIR=/data/images','LOG_DIR=/data/logs'])+'\n');env.chmod(0o600)
name='codex2api-release-smoke-'+manifest['revision'][:8];base='http://127.0.0.1:18769';checks={'result':'failed'};started=time.monotonic();created=False
def request(path,body=None,auth=True):
    headers={'X-Admin-Key':admin} if auth else {}
    if body is not None:headers['Content-Type']='application/json'
    req=urllib.request.Request(base+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method='PUT' if body is not None else 'GET')
    try:
        with urllib.request.urlopen(req,timeout=10) as r:return r.status,dict(r.headers),r.read()
    except urllib.error.HTTPError as e:return e.code,dict(e.headers),e.read()
def settings(body=None):
    status,_,payload=request('/api/admin/settings/basispoints',body);assert status==200,status;return json.loads(payload)
def run(*args):return subprocess.check_output(args,stderr=subprocess.PIPE)
try:
    run('docker','run','-d','--name',name,'--platform','linux/amd64','--env-file',str(env),'-p','127.0.0.1:18769:8080','-v',str(data)+':/data',manifest['digest'])
    created=True
    deadline=time.monotonic()+90
    while True:
        try:
            if request('/health',auth=False)[0]==200:break
        except Exception:pass
        state=json.loads(run('docker','inspect',name))[0]['State']
        if not state['Running']:raise RuntimeError('isolated OCI exited with code '+str(state['ExitCode']))
        if time.monotonic()>deadline:raise RuntimeError('isolated OCI readiness failed')
        time.sleep(.5)
    actual=json.loads(run('docker','inspect',name))[0];assert actual['Image']==manifest['digest'];checks['same_digest']=True
    info=json.loads(request('/api/admin/system/build')[2]);assert info['source_revision']==manifest['revision'] and info['source_tree']==manifest['tree'];checks['provenance']=True
    for path in ['/api/admin/settings/basispoints','/api/admin/account-ops/module','/api/admin/system/build']:
        assert request(path,auth=False)[0]==401;assert request(path)[0]==200
    checks['authenticated_admin']=True
    payload={'enabled':True,'model_scope':'selected','models':['gpt-5.6-sol'],'image_relay_enabled':True,'image_relay_public_origin':'https://relay.example.com'}
    s=settings(payload);assert s['image_relay_runtime']['signing_key_configured'] and s['image_relay_runtime']['validation_status']=='configured_unverified';checks['settings_runtime']=True
    blob=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=');(data/'images'/'smoke.png').write_bytes(blob)
    expiry=int(time.time())+600
    with sqlite3.connect(data/'smoke.db',timeout=10) as db:
        cursor=db.execute("INSERT INTO image_assets(filename,storage_path,mime_type,bytes,model,relay_expires_at,relay_epoch,relay_state) VALUES(?,?,?,?,?,?,?,?)",('smoke.png','/data/images/smoke.png','image/png',len(blob),'bps-inbound',expiry,s['image_relay_epoch'],'ready'));asset=cursor.lastrowid
    db.close()
    signature=hmac.new(hashlib.sha256(signing.encode()).digest(),f'{asset}|{expiry}|0'.encode(),hashlib.sha256).digest()[:16].hex();url=f'/p/img/{asset}?exp={expiry}&sig={signature}'
    status,headers,body=request(url,auth=False);assert status==200,(status,body[:150]);assert body==blob;assert 'no-store' in headers.get('Cache-Control','');assert headers.get('X-Content-Type-Options')=='nosniff';checks['signed_asset_stream']=True
    payload['enabled']=False;s2=settings(payload);assert s2['image_relay_epoch']>s['image_relay_epoch'];assert request(url,auth=False)[0] in (403,404)
    payload['enabled']=True;settings(payload);assert request(url,auth=False)[0] in (403,404);checks['disable_reenable_revokes_old_link']=True
    html=request('/admin/settings',auth=False)[2].decode();srcs=re.findall(r'<script[^>]+src="([^"]+)"',html);assert srcs
    for src in srcs:assert request(src,auth=False)[0]==200
    checks['ui_shell_and_javascript']=True
    logs=run('docker','logs',name).decode(errors='replace');assert signature not in logs and signing not in logs and admin not in logs;checks['credentials_absent_from_logs']=True
    checks['result']='passed'
finally:
    if created:subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    env.unlink(missing_ok=True)
    checks.update({'digest':manifest['digest'],'revision':manifest['revision'],'elapsed_seconds':round(time.monotonic()-started,2)})
    (root/'result.json').write_text(json.dumps(checks,indent=2)+'\n')
print(json.dumps(checks))
