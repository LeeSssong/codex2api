import React from 'react';

export type AutoConfigProps = { value: { enabled:boolean; platform:string; priority:number; concurrency:number; load_factor:number }; onChange: (next: AutoConfigProps['value']) => void };
export default function AutoConfig({value,onChange}: AutoConfigProps) {
  return <section><h1>OAuth auto configuration</h1><label><input type="checkbox" checked={value.enabled} onChange={e=>onChange({...value,enabled:e.target.checked})}/> Enabled</label><label>Platform <input value={value.platform} onChange={e=>onChange({...value,platform:e.target.value})}/></label><label>Priority <input type="number" min={0} value={value.priority} onChange={e=>onChange({...value,priority:Number(e.target.value)})}/></label><label>Concurrency <input type="number" min={1} value={value.concurrency} onChange={e=>onChange({...value,concurrency:Number(e.target.value)})}/></label></section>;
}
