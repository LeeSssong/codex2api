import React from 'react';
import { putPriorityScheduling } from '../api';
export type PrioritySchedulingProps = { enabled:boolean; mode:string; onChange:(next:{enabled:boolean;mode:string})=>void };
export { putPriorityScheduling };
export default function PriorityScheduling({enabled,mode,onChange}:PrioritySchedulingProps) { return <section><h1>Priority scheduling</h1><label><input type="checkbox" checked={enabled} onChange={e=>onChange({enabled:e.target.checked,mode})}/> Enabled</label><label>Mode <select value={mode} onChange={e=>onChange({enabled,mode:e.target.value})}><option value="experience">Experience</option><option value="balanced">Balanced</option><option value="profit">Profit</option><option value="custom">Custom</option></select></label></section>; }
