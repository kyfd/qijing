// Agent 巡视流程：payload 预览（先看再确认）→ 运行 → 取消 → 报告。
// Agent 输出只作为观察与建议呈现，这里没有任何文件操作入口。
import { state } from '../state/state.js';
import { $, escapeHtml, toast, formatBytes, renderMarkdown } from '../components/dom.js';
import { adapter } from '../api/adapter.js';
import { explainAgentError } from './profile.js';
import { switchSettingsTab } from '../settings/settings.js';

export function payloadText(payload) { return typeof payload === 'string' ? payload : JSON.stringify(payload || {}, null, 2); }

function previewFields(data = {}) {
  const payload = data.payload ?? data.body ?? data.agent_payload ?? {};
  const text = payloadText(payload);
  return { raw: data, payload, text, target: data.target_origin || data.target || data.origin || data.base_url || state.model.base_url || (state.model.provider_type === 'local' ? '本地模型' : '未配置'), hash: data.hash || data.payload_hash || '由服务端确认时校验', bytes: Number(data.payload_bytes || data.bytes || data.size || new TextEncoder().encode(text).length), confirmation: data.confirmation_token || data.confirmation || data.token || '' };
}

export async function openAgentPreview() {
  if (!state.nodes.length) { toast('请先完成一次只读扫描，再让 Agent 巡视'); return; }
  if (!state.model.model) { $('#settingsDialog').showModal(); switchSettingsTab('agent'); toast('请先配置 Agent 模型'); return; }
  $('#agentPreviewDialog').showModal(); $('#agentPayloadPreview').textContent = '正在由本地服务生成匿名 payload…'; $('#confirmAgentBtn').disabled = true;
  try {
    state.agent.preview = previewFields(await adapter.previewAgentRun());
    if (!state.agent.preview.confirmation || !state.agent.preview.hash || state.agent.preview.hash === '由服务端确认时校验') throw new Error('预览缺少确认令牌或 Payload Hash');
    state.agent.lastPayload = state.agent.preview.payload;
    $('#previewTarget').textContent = state.agent.preview.target; $('#previewHash').textContent = state.agent.preview.hash; $('#previewHash').title = state.agent.preview.hash; $('#previewSize').textContent = formatBytes(state.agent.preview.bytes); $('#agentPayloadPreview').textContent = state.agent.preview.text; $('#confirmAgentBtn').disabled = false;
  } catch (error) { $('#agentPayloadPreview').textContent = `无法生成预览：${error.message || '本地服务尚未支持 Agent API'}`; }
}

function openAgentDrawer() { $('#agentDrawer').classList.add('open'); $('#agentDrawer').setAttribute('aria-hidden','false'); $('#backdrop').hidden=false; }

export function renderAgentSteps(steps = [], runStatus = '') {
  const failed = ['failed', 'error'].includes(String(runStatus).toLowerCase());
  $('#agentSteps').innerHTML = steps.map((step,i) => {
    const kind = String(step.kind || '');
    let status = step.status || step.state || (step.completed ? 'done' : (kind === 'error' ? 'failed' : (kind === 'response' ? 'done' : 'pending')));
    if (failed && kind === 'request' && i === steps.length - 1) status = 'failed';
    const label = status === 'failed' ? '失败' : (status === 'done' || status === 'completed' ? '完成' : (status === 'running' ? '运行中' : '进行中'));
    return `<div class="agent-step ${escapeHtml(status)}"><i>${status==='done'||status==='completed'?'✓':(status==='failed'?'!':i+1)}</i><div><strong>${escapeHtml(step.title||step.name||step.tool||`分析步骤 ${i+1}`)}</strong><small>${escapeHtml(step.detail||step.summary||step.message||'等待 Agent')}</small></div><span>${escapeHtml(step.duration_ms ? `${step.duration_ms} ms` : label)}</span></div>`;
  }).join('') || '<div class="agent-step running"><i>1</i><div><strong>建立只读调查计划</strong><small>Agent 正在检查已确认的匿名证据</small></div><span>运行中</span></div>';
}

export async function confirmAgentRun() {
  const preview = state.agent.preview; if (!preview) return;
  $('#confirmAgentBtn').disabled = true;
  try {
    const result = await adapter.startAgentRun(preview.hash, preview.confirmation);
    state.agent.runId = result.run_id || result.id || ''; state.agent.running = true; state.agent.startedAt = Date.now();
    $('#agentPreviewDialog').close(); openAgentDrawer(); $('#agentRunState').textContent = '巡视中'; $('#cancelAgentBtn').hidden = false; $('#agentReport').hidden = true; renderAgentSteps(result.steps || []); pollAgentRun();
  } catch (error) { $('#confirmAgentBtn').disabled = false; toast(`无法开始 Agent 巡视：${error.message || '确认已失效'}`); }
}

function applyAgentRun(data = {}) {
  const status = String(data.status || data.state || 'running').toLowerCase(), steps = data.steps || [];
  renderAgentSteps(steps, status); const done = steps.filter(s => ['done','completed','success'].includes(String(s.status||s.state||s.kind).toLowerCase()) || String(s.kind).toLowerCase()==='response').length; $('#agentProgressBar').style.width = `${status==='completed'||status==='done'||status==='failed'?100:Math.max(8,steps.length ? done/Math.max(steps.length,1)*100 : 12)}%`;
  $('#agentRunState').textContent = ({completed:'已完成',done:'已完成',failed:'失败',cancelled:'已取消',canceled:'已取消',running:'巡视中',cancelling:'取消中'})[status] || status;
  $('#agentRunState').classList.toggle('failed', status==='failed');
  const errBox = $('#agentRunError');
  const errText = data.error || data.message || data.last_error || '';
  if (status === 'failed' && errText) {
    errBox.hidden = false;
    errBox.textContent = explainAgentError({ message: errText });
  } else {
    errBox.hidden = true;
    errBox.textContent = '';
  }
  const usage = data.usage || {}; $('#agentTokens').textContent = (usage.total_tokens ?? data.tokens ?? '—').toLocaleString?.('zh-CN') || '—'; $('#agentDuration').textContent = data.duration_ms ? `${(data.duration_ms/1000).toFixed(1)} s` : `${((Date.now()-state.agent.startedAt)/1000).toFixed(1)} s`; $('#agentConfidence').textContent = data.confidence != null ? `${Math.round(Number(data.confidence)* (Number(data.confidence)<=1?100:1))}%` : '—';
  return ['completed','done','failed','cancelled','canceled'].includes(status);
}

export async function pollAgentRun() {
  clearTimeout(state.agent.timer);
  try {
    const data = await adapter.agentRunStatus(state.agent.runId);
    try { const audit = await adapter.listAgentAudits(state.agent.runId); data.steps = Array.isArray(audit) ? audit : (audit.steps || []); } catch (_) {}
    if (!applyAgentRun(data)) { state.agent.timer = setTimeout(pollAgentRun, 700); return; }
    state.agent.running = false; $('#cancelAgentBtn').hidden = true;
    if (['completed','done','failed'].includes(String(data.status||data.state).toLowerCase())) await loadAgentResult(data);
  } catch (error) { state.agent.running=false; $('#agentRunState').textContent='状态异常'; $('#agentRunState').classList.add('failed'); $('#cancelAgentBtn').hidden=true; toast(`无法读取 Agent 状态：${error.message}`); }
}

async function loadAgentResult(statusData = {}) {
  let data = statusData; try { data = { ...statusData, ...await adapter.agentRunResult(state.agent.runId) }; } catch (_) {}
  let report = data.report || data.result || {};
  if (typeof report === 'string') { try { report = JSON.parse(report); } catch (_) { report = { text: report }; } }
  const failed = ['failed','error'].includes(String(data.status||data.state||'').toLowerCase());
  const body = report.final?.content || report.summary || report.markdown || report.text || report.overview || (failed ? (data.error || '巡视失败，模型没有返回报告。') : 'Agent 已完成巡视，但未返回报告正文。');
  $('#agentReportBody').innerHTML = `<div class="report-prose">${renderMarkdown(body)}</div>`;
  const evidence = report.evidence || report.tools_called || data.evidence || []; $('#agentEvidence').innerHTML = evidence.length ? evidence.map(item=>`<div class="evidence-item">${escapeHtml(typeof item==='string'?item:(item.summary||item.description||JSON.stringify(item)))}</div>`).join('') : '<p class="quiet">报告未引用额外证据</p>'; $('#agentReport').hidden=false; applyAgentRun(data);
}

export async function cancelAgentRun() { if(!state.agent.running)return; $('#agentRunState').textContent='取消中'; try{await adapter.cancelAgentRun(state.agent.runId);pollAgentRun();}catch(error){toast(`取消巡视失败：${error.message}`);} }
