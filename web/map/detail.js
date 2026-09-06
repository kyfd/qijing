// 节点详情抽屉：展示证据与元数据，动作只限于“定位/忽略/稍后”，
// 永远不会在这里执行修改文件的操作。
import { state, zones } from '../state/state.js';
import { $, escapeHtml, toast, formatBytes } from '../components/dom.js';
import { adapter, desktopError } from '../api/adapter.js';

export async function openDetail(node) {
  let detail=node; if(!state.demo){try{detail={...node,...await adapter.node(node.id)};}catch(error){if(adapter.mode==='desktop')toast(desktopError('读取节点详情',error));}}
  const meta=zones[detail.zone]||zones.active;
  $('#drawerContent').innerHTML=`<span class="drawer-zone"><i style="background:${meta.color}"></i>${meta.name}</span><h2>${escapeHtml(detail.name)}</h2><p class="detail-path">${escapeHtml(detail.path||'路径未提供')}</p><div class="detail-metrics"><div class="detail-metric"><span class="metric-label">占用空间</span><strong class="metric-value">${formatBytes(detail.size)}</strong></div><div class="detail-metric"><span class="metric-label">生态健康度</span><strong class="metric-value">${detail.health ?? '—'} / 100</strong></div><div class="detail-metric"><span class="metric-label">类型</span><strong class="metric-value">${escapeHtml(detail.kind||'文件')}</strong></div><div class="detail-metric"><span class="metric-label">最近变化</span><strong class="metric-value">${escapeHtml(detail.modified||'未知')}</strong></div></div><section class="drawer-section"><h3>Agent 观察</h3><p>${escapeHtml(detail.insight||meta.description)}。此处只呈现观察结果，不会自动执行任何文件操作。</p></section><div class="drawer-actions"><button class="primary" data-action="reveal">在资源管理器中定位</button><button data-action="ignore">忽略建议</button><button data-action="later">稍后处理</button></div>`;
  $('#detailDrawer').dataset.id=detail.id;$('#detailDrawer').classList.add('open');$('#detailDrawer').setAttribute('aria-hidden','false');$('#backdrop').hidden=false;
}

export function closeDrawer(){ $('#detailDrawer').classList.remove('open');$('#detailDrawer').setAttribute('aria-hidden','true');$('#backdrop').hidden=true; }

export async function drawerAction(action) {
  const id=$('#detailDrawer').dataset.id,node=state.allNodes.find(n=>String(n.id)===String(id));
  if(action==='ignore'){try{if(!state.demo)await adapter.ignoreRecommendation(id);toast('已忽略这条建议');closeDrawer();}catch(error){toast(adapter.mode==='desktop'?desktopError('忽略建议',error):'暂时无法保存忽略状态');}}
  if(action==='later') toast('已留在稍后清单，不会修改文件');
  if(action==='reveal'){try{if(!state.demo)await adapter.revealNode(id);else throw new Error();toast('已请求资源管理器定位');}catch(error){if(node?.path && navigator.clipboard)navigator.clipboard.writeText(node.path).catch(()=>{});toast(adapter.mode==='desktop'?desktopError('在资源管理器中定位',error):'路径已复制，可在资源管理器中打开');}}
}
