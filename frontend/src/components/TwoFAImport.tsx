import { useState } from 'react'
import { KeyRound, Upload } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

export default function TwoFAImport({ accountId, onSaved }: { accountId: number; onSaved?: () => void }) {
  const [email, setEmail] = useState(''); const [password, setPassword] = useState(''); const [totp, setTotp] = useState(''); const [busy, setBusy] = useState(false)
  async function save() { setBusy(true); try { const r=await fetch(`/api/admin/accounts/${accountId}/credential-ops/config`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({email,password,totp_secret:totp,mode:'password_totp',engine:'local_worker'})}); if(!r.ok) throw new Error(await r.text()); onSaved?.() } finally { setBusy(false) } }
  return <div className="space-y-3 rounded-md border p-4"><div className="flex items-center gap-2 font-semibold"><KeyRound className="size-4"/>2FA login configuration</div><Input value={email} onChange={e=>setEmail(e.target.value)} placeholder="Login email" autoComplete="off"/><Input value={password} onChange={e=>setPassword(e.target.value)} placeholder="Password" type="password" autoComplete="new-password"/><Input value={totp} onChange={e=>setTotp(e.target.value)} placeholder="TOTP secret" type="password" autoComplete="off"/><Button onClick={()=>void save()} disabled={busy||!email||!password||!totp}><Upload className="size-4"/>{busy?'Saving...':'Save encrypted config'}</Button></div>
}
