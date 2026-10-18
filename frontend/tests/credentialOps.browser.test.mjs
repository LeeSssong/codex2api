import assert from 'node:assert/strict'
import { before, after, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { mkdir } from 'node:fs/promises'
import { build } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'

const root = fileURLToPath(new URL('../', import.meta.url))
const artifacts = path.resolve(root, '../.superpowers/sdd/2026-10-18-credential-operations/ui')
let browser, script, css
before(async () => {
 await mkdir(artifacts, {recursive:true})
 const output = await build({configFile:false, root, logLevel:'silent', plugins:[react(),tailwindcss(),{
  name:'credential-fixture',enforce:'pre',
  resolveId(id){if(id==='virtual:credential-fixture')return '\0credential-fixture'},
  load(id){if(id!=='\0credential-fixture')return;return `
   import React from 'react'; import {createRoot} from 'react-dom/client';
   import {MemoryRouter} from 'react-router-dom';
   import CredentialOps from ${JSON.stringify(path.join(root,'src/pages/CredentialOps.tsx'))};
   import TwoFAImport from ${JSON.stringify(path.join(root,'src/components/TwoFAImport.tsx'))};
   import CredentialTOTPDialog from ${JSON.stringify(path.join(root,'src/components/CredentialTOTPDialog.tsx'))};
   import CredentialConfigEditor from ${JSON.stringify(path.join(root,'src/components/CredentialConfigEditor.tsx'))};
   import {ToastProvider} from ${JSON.stringify(path.join(root,'src/components/ToastProvider.tsx'))};
   import ${JSON.stringify(path.join(root,'src/i18n.ts'))}; import ${JSON.stringify(path.join(root,'src/index.css'))};
   function ConfigFixture(){const [open,setOpen]=React.useState(true);return open?React.createElement(React.Fragment,null,React.createElement('button',{onClick:()=>setOpen(false)},'Close config fixture'),React.createElement(CredentialConfigEditor,{accountId:1,email:'demo@example.test',studioConfigured:true,globalEngine:window.__globalEngine})):React.createElement('button',{onClick:()=>setOpen(true)},'Reopen config fixture');}
   function TotpFixture(){const [open,setOpen]=React.useState(true);return open?React.createElement(CredentialTOTPDialog,{accountId:1,name:'Fixture',onClose:()=>setOpen(false)}):null;}
   createRoot(document.getElementById('root')).render(React.createElement(MemoryRouter,{initialEntries:[window.__initialPath||'/']},React.createElement(ToastProvider,null,React.createElement('main',{style:{padding:'24px',maxWidth:'1440px',margin:'0 auto'}},window.__surface==='import'?React.createElement(TwoFAImport):window.__surface==='totp'?React.createElement(TotpFixture):window.__surface==='config'?React.createElement(ConfigFixture):React.createElement(CredentialOps)))));`;}
 }],resolve:{alias:{'@':path.join(root,'src')}},define:{'process.env.NODE_ENV':'"production"'},build:{write:false,minify:false,rollupOptions:{input:'virtual:credential-fixture'},lib:{entry:'virtual:credential-fixture',formats:['iife'],name:'CredentialFixture'}}})
 const bundle=Array.isArray(output)?output[0]:output
 script=bundle.output.find(value=>value.type==='chunk').code
 css=bundle.output.filter(value=>value.type==='asset'&&value.fileName.endsWith('.css')).map(value=>value.source).join('\n')
 browser=await chromium.launch({headless:true})
})
after(async()=>{await browser?.close()})
async function fixture(t,{surface='operations',mobile=false,dark=false,encryption=true,exportDelay=0,globalEngine='local_worker',savedEngine='local_worker',initialPath='/',savedProxySource='proxy',savedProxyID=9,overviewAccounts}={}){
 const page=await browser.newPage({viewport:mobile?{width:390,height:844}:{width:1440,height:960},colorScheme:dark?'dark':'light'})
 page.setDefaultTimeout(6000); t.after(()=>page.close())
 const errors=[];page.on('pageerror',error=>errors.push(error.message));t.after(()=>assert.deepEqual(errors,[]))
 const calls=[]
 let exported=0
 let config={account_id:1,email:'demo@example.test',mode:'password_totp',engine:savedEngine,proxy_source:savedProxySource,proxy_id:savedProxyID,password_configured:true,totp_configured:true,otp_url_configured:false,revision:1}
 await page.route('http://credential.test/**',async route=>{
  const req=route.request(); const url=new URL(req.url());const p=url.pathname.replace('/api/admin','');const body=req.postDataJSON?.()
  if(!url.pathname.startsWith('/api/'))return route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="root"></div></body></html>'})
  calls.push({path:p,method:req.method(),body})
  const ok=data=>route.fulfill({contentType:'application/json',body:JSON.stringify(data)})
  if(p==='/credential-ops/encryption')return ok({configured:encryption,source:'local_file'})
  if(p==='/credential-ops/encryption/initialize'){encryption=true;return ok({configured:true,source:'local_file'})}
  if(p==='/credential-ops/mihomo')return ok({configured:false,installed:false,node_count:0,nodes:[],active:0,concurrency:3,binary:'/data/credential-mihomo/mihomo'})
  if(p==='/account-groups')return ok({groups:[]})
  if(p==='/proxies')return ok({proxies:[{id:9,label:'Node 9',enabled:true}]})
  if(p==='/accounts/1/credential-ops/config'){if(req.method()==='PUT')config={...config,...body,revision:config.revision+1};return ok(config)}
  if(p==='/accounts/1/credential-ops/login'){config={...config,...body,revision:config.revision+1};return ok({ID:43,AccountID:1,Status:'queued',Stage:'queued',ConfigRevision:config.revision})}
  if(p==='/credential-ops/login/42/retry')return ok({ID:44,AccountID:0,Status:'queued',Stage:'queued'})
  if(p==='/credential-ops/import')return ok({ID:41,AccountID:0,Status:'queued',Stage:'queued',Attempt:0,CreatedAt:new Date().toISOString()})
  if(p==='/credential-ops/login/41')return ok({ID:41,AccountID:1,Status:'succeeded',Stage:'succeeded',Attempt:1,CreatedAt:new Date().toISOString()})
  if(p.endsWith('/totp/export')){exported++;if(exportDelay)await new Promise(resolve=>setTimeout(resolve,exportDelay));return ok({email:'demo@example.test',totp_secret:'JBSWY3DPEHPK3PXP'})}
  if(p.endsWith('/totp'))return ok({status:'idle',candidate_available:false,previous_available:false})
  if(p==='/credential-ops')return ok({enabled:true,accounts:overviewAccounts||[{account_id:1,name:'演示 Plus 账号',email:'demo@example.test',enabled:true,auto_relogin:true,probe_state:'ok',probe_detail:'',fail_streak:0,last_probe_at:new Date().toISOString(),next_probe_at:new Date().toISOString(),cooldown_until:new Date(Date.now()+1800000).toISOString(),config}],tasks:[{ID:42,AccountID:0,Status:'failed',Stage:'failed',Kind:'login',CreatedAt:new Date().toISOString()}],rules:{interval_seconds:1800,retry_seconds:300,failure_threshold:2,cooldown_seconds:1800},login_configured:{'1':true},session_studio_configured:true,runtime_settings:{engine:globalEngine,worker_concurrency:3},worker:{state:'running',mode:'managed',capabilities:['login_codex_v1','totp_rotation_v1']},encryption:{configured:true,source:'local_file'}})
  return route.fulfill({status:404,contentType:'application/json',body:'{"error":"Unexpected fixture API"}'})
 })
 await page.goto('http://credential.test/')
 await page.evaluate(({surface,dark,initialPath,globalEngine})=>{localStorage.setItem('lang','zh');localStorage.setItem('admin_key','fixture');window.__surface=surface;window.__initialPath=initialPath;window.__globalEngine=globalEngine;if(dark)document.documentElement.classList.add('dark')},{surface,dark,initialPath,globalEngine})
 await page.addStyleTag({content:css});await page.addScriptTag({content:script})
 return {page,calls,exports:()=>exported}
}

test('first-import form queues password and TOTP then clears secrets',async t=>{
 const {page,calls}=await fixture(t,{surface:'import',encryption:false})
 await page.getByRole('button',{name:'一键启用加密'}).click()
 await page.getByLabel('登录邮箱',{exact:true}).fill('demo@example.test')
 await page.getByLabel('登录密码',{exact:true}).fill('my password')
 await page.getByLabel('2FA（TOTP）长期密钥',{exact:true}).fill('JBSWY3DPEHPK3PXP')
 await page.getByRole('button',{name:'登录并导入',exact:true}).click()
 await page.getByText(/等待执行|已完成并保存/).waitFor()
 const queued=calls.find(call=>call.path==='/credential-ops/import')
 assert.equal(queued.body.password,'my password');assert.equal(queued.body.totp_secret,'JBSWY3DPEHPK3PXP');assert.match(queued.body.import_key,/^[0-9a-f-]{36}$/)
 assert.equal(await page.locator('input[type=password]').count(),0)
 await page.getByText(/已完成并保存/).waitFor()
 assert.equal(calls.filter(call=>call.path==='/credential-ops/import').length,1)
 const storage=await page.evaluate(()=>JSON.stringify(localStorage));assert.ok(!storage.includes('my password')&&!storage.includes('JBSWY3DPEHPK3PXP'))
})

test('batch first import validates duplicate addresses before creating jobs',async t=>{
 const {page,calls}=await fixture(t,{surface:'import'})
 await page.getByRole('button',{name:'批量导入',exact:true}).click()
 await page.getByLabel('账号登录资料',{exact:true}).fill('demo@example.test----secret\nDEMO@example.test----other')
 await page.getByRole('button',{name:'登录并导入',exact:true}).click()
 await page.getByRole('alert').filter({hasText:'邮箱重复'}).waitFor()
 assert.equal(calls.filter(call=>call.path==='/credential-ops/import').length,0)
})

test('operations center renders desktop and mobile without document overflow',async t=>{
 for(const mobile of [false,true]){
  const {page}=await fixture(t,{mobile,dark:mobile})
  await page.getByRole('heading',{name:'凭证运营',exact:true}).waitFor()
  const account=page.getByText('演示 Plus 账号',{exact:true}).filter({visible:true})
  await account.waitFor()
  if(mobile)assert.ok((await account.boundingBox()).width>=140,'mobile account identity must remain readable')
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))
  await page.screenshot({path:path.join(artifacts,mobile?'operations-mobile.png':'operations-desktop.png'),fullPage:true})
 }
})

test('2FA status never fetches secrets until explicit export',async t=>{
 const {page,exports}=await fixture(t,{surface:'totp'})
 await page.getByRole('button',{name:'显示并导出凭据',exact:true}).waitFor()
 assert.equal(exports(),0)
 await page.getByRole('button',{name:'显示并导出凭据',exact:true}).click()
 await page.getByAltText('2FA 验证器二维码').waitFor()
 assert.equal(exports(),1)
 assert.equal(await page.getByText('JBSWY3DPEHPK3PXP',{exact:true}).count(),1)
 await page.screenshot({path:path.join(artifacts,'2fa-export.png'),fullPage:true})
})

test('closing 2FA export discards a late secret response', async t => {
 const {page,exports}=await fixture(t,{surface:'totp',exportDelay:150})
 await page.getByRole('button',{name:'显示并导出凭据',exact:true}).click()
 await page.getByRole('button',{name:'Close',exact:true}).click()
 await page.waitForTimeout(250)
 assert.equal(exports(),1)
 assert.equal(await page.getByText('JBSWY3DPEHPK3PXP',{exact:true}).count(),0)
 assert.equal(await page.getByAltText('2FA 验证器二维码').count(),0)
})

test('task history retries a failed first import after reopening without re-entering credentials',async t=>{
 const {page,calls}=await fixture(t,{initialPath:'/?view=tasks'})
 await page.getByRole('button',{name:'重试',exact:true}).click()
 assert.equal(calls.filter(call=>call.path==='/credential-ops/login/42/retry').length,1)
 assert.equal(calls.filter(call=>call.path==='/credential-ops/import').length,0)
})

test('save-and-login refreshes the configuration revision before another save',async t=>{
 const {page,calls}=await fixture(t,{surface:'config'})
 await page.getByLabel('登录密码',{exact:true}).fill('replacement')
 await page.getByRole('button',{name:'保存并重登',exact:true}).click()
 await page.getByText('重登任务已排队',{exact:true}).waitFor()
 await page.getByLabel('登录密码',{exact:true}).fill('next-replacement')
 await page.getByRole('button',{name:'保存登录配置',exact:true}).click()
 await page.getByText('登录配置已保存',{exact:true}).waitFor()
 assert.equal(calls.find(call=>call.path.endsWith('/config')&&call.method==='PUT').body.revision,2)
})

test('clear TOTP excludes an entered replacement',async t=>{
 const {page,calls}=await fixture(t,{surface:'config'})
 await page.getByLabel('2FA（TOTP）长期密钥',{exact:true}).fill('JBSWY3DPEHPK3PXP')
 await page.getByRole('switch',{name:'清除已保存的 TOTP 密钥'}).click()
 await page.getByRole('button',{name:'保存登录配置',exact:true}).click()
 await page.getByText('登录配置已保存',{exact:true}).waitFor()
 const saved=calls.find(call=>call.path.endsWith('/config')&&call.method==='PUT').body
 assert.equal(saved.totp_secret,'');assert.equal(saved.clear_totp,true)
})

test('engine roundtrip retains the selected local egress',async t=>{
 const {page,calls}=await fixture(t,{surface:'config'})
 await page.getByRole('button',{name:'重登引擎',exact:true}).click()
 await page.getByRole('option',{name:'Session Studio',exact:true}).click()
 await page.getByRole('button',{name:'重登引擎',exact:true}).click()
 await page.getByRole('option',{name:'本地 Worker',exact:true}).click()
 await page.getByRole('button',{name:'保存登录配置',exact:true}).click()
 await page.getByText('登录配置已保存',{exact:true}).waitFor()
 const saved=calls.find(call=>call.path.endsWith('/config')&&call.method==='PUT').body
 assert.equal(saved.proxy_source,'proxy');assert.equal(saved.proxy_id,9)
})

test('inherited Session Studio is disclosed in first import and saved configuration',async t=>{
 for(const surface of ['import','config']){
  const {page}=await fixture(t,{surface,globalEngine:'session_studio',savedEngine:'inherit'})
  await page.getByText(/邮箱、密码及 TOTP 密钥会发送到所配置的服务/).waitFor()
 }
})

test('operator diagnostics include secret presence, cooldown and Worker capabilities',async t=>{
 const accounts=await fixture(t)
 await accounts.page.getByText(/TOTP 已配置/).filter({visible:true}).waitFor()
 await accounts.page.getByText(/冷却至/).filter({visible:true}).waitFor()
 const runtime=await fixture(t,{initialPath:'/?view=runtime'})
 await runtime.page.getByText(/Worker 支持能力.*Codex 登录.*2FA 更换与恢复/).waitFor()
})

test('nonstatic proxy selector survives save reopen and login using server config shape',async t=>{
 const {page,calls}=await fixture(t,{surface:'config'})
 await page.getByRole('button',{name:'登录出口',exact:true}).click()
 await page.getByRole('option',{name:'直连',exact:true}).click()
 await page.getByRole('button',{name:'保存登录配置',exact:true}).click()
 await page.getByText('登录配置已保存',{exact:true}).waitFor()
 await page.getByRole('button',{name:'Close config fixture',exact:true}).click()
 await page.getByRole('button',{name:'Reopen config fixture',exact:true}).click()
 await page.getByRole('button',{name:'登录出口',exact:true}).filter({hasText:'直连'}).waitFor()
 await page.getByRole('button',{name:'保存并重登',exact:true}).click()
 await page.getByText('重登任务已排队',{exact:true}).waitFor()
 const login=calls.find(call=>call.path.endsWith('/login')&&call.method==='POST')
 assert.equal(login.body.proxy_source,'direct');assert.equal(login.body.proxy_id,null)
})
test('nonstatic selector ignores legacy stale static ID after reopening',async t=>{
 const {page,calls}=await fixture(t,{surface:'config',savedProxySource:'direct',savedProxyID:9})
 await page.getByRole('button',{name:'登录出口',exact:true}).filter({hasText:'直连'}).waitFor()
 await page.getByRole('button',{name:'保存并重登',exact:true}).click()
 await page.getByText('重登任务已排队',{exact:true}).waitFor()
 const login=calls.find(call=>call.path.endsWith('/login')&&call.method==='POST')
 assert.equal(login.body.proxy_source,'direct');assert.equal(login.body.proxy_id,null)
})
test('backend effective_egress diagnostics render on desktop and mobile',async t=>{
 for(const mobile of [false,true]){
  const accounts=['direct','proxy #9','mihomo'].map((egress,index)=>({account_id:index+1,name:'egress '+egress,email:'route'+index+'@example.test',enabled:true,auto_relogin:true,probe_state:'ok',probe_detail:'',fail_streak:0,next_probe_at:new Date().toISOString(),effective_engine:'local_worker',effective_egress:egress}))
  const {page}=await fixture(t,{mobile,overviewAccounts:accounts})
  for(const egress of ['direct','proxy #9','mihomo'])await page.getByText(egress,{exact:true}).filter({visible:true}).waitFor()
 }
})
