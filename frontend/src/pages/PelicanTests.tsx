import React from 'react';
export type PelicanTestsProps = { jobs:Array<{id:number;status:string;model:string;cost?:number}>; onCancel:(id:number)=>void };
export default function PelicanTests({jobs,onCancel}:PelicanTestsProps) { return <section><h1>Pelican tests</h1><table><thead><tr><th>Job</th><th>Model</th><th>Status</th><th>Cost</th><th /></tr></thead><tbody>{jobs.map(job=><tr key={job.id}><td>{job.id}</td><td>{job.model}</td><td>{job.status}</td><td>{job.cost ?? 0}</td><td><button type="button" onClick={()=>onCancel(job.id)}>Cancel</button></td></tr>)}</tbody></table></section>; }
