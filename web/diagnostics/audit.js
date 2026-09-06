// 隐私与诊断视图：本地能力边界审计、Agent payload 审计与运行记录。
// 数据全部来自本机；展示前经过脱敏字段，不引入新的网络访问。
import { state } from '../state/state.js';
import { $, escapeHtml, toast, formatBytes } from '../components/dom.js';
import { adapter, desktopError } from '../api/adapter.js';
import { payloadText } from '../agent/run.js';

export async function openPrivacy(){
  $('#privacyDialog').showModal();
  try { const data=await adapter.privacy(); if(data.capabilities) $('#privacyAudit').innerHTML=Object.entries(data.capabilities).map(([k,v])=>`<div><span>${escapeHtml(k)}</span><strong class="${v===false?'safe':''}">${escapeHtml(v===false?'禁用':String(v))}</strong></div>`).join(''); const payload=data.agent_payload||data.payload||state.agent.lastPayload; $('#privacyPayload').textContent=payload?payloadText(payload):'尚无可展示的 payload'; }
  catch(error){if(adapter.mode==='desktop')toast(desktopError('读取隐私审计',error));}
  try { const data=state.agent.runId ? await adapter.listAgentAudits(state.agent.runId) : {steps:[]}; state.agent.audits=Array.isArray(data)?data:(data.runs||data.audits||data.steps||[]); $('#auditRuns').innerHTML=state.agent.audits.length?state.agent.audits.map(run=>`<div class="audit-run"><strong>${escapeHtml(run.model||run.target||run.name||'Agent 巡视')}</strong><span>${escapeHtml(run.status||run.state||run.kind||'已记录')}</span><small>${escapeHtml(run.created_at||run.time||run.at||'')} · ${escapeHtml(run.payload_hash||run.detail||'匿名步骤')} · ${formatBytes(run.payload_bytes||run.bytes||0)}</small></div>`).join(''):'<p class="quiet">尚无 Agent 运行记录</p>'; } catch(_) {}
}

export function switchAuditTab(name){document.querySelectorAll('[data-audit-tab]').forEach(b=>b.classList.toggle('active',b.dataset.auditTab===name));$('#boundaryAudit').hidden=name!=='boundary';$('#payloadAudit').hidden=name!=='payload';$('#runsAudit').hidden=name!=='runs';}
