// 地图周边的信息面板：分区汇总侧栏、统计条、空态与节点搜索。
import { state, zones } from '../state/state.js';
import { $, escapeHtml, formatBytes } from '../components/dom.js';
import { ensureAnim, stopAnim } from './render.js';

export function renderEmpty(show) { $('#emptyState').hidden = !show; if (show) stopAnim(); else ensureAnim(); }

export function updateStats(stats = {}) {
  const count = stats.files ?? stats.count ?? state.allNodes.length;
  const bytes = stats.bytes ?? stats.total_bytes ?? state.allNodes.reduce((s,n) => s + n.size, 0);
  const recs = stats.recommendations ?? state.recommendations.length;
  // The canvas only draws the largest entries, so say what it leaves out
  // rather than letting the file count imply everything is on screen.
  const omitted = state.truncation ? `<span class="stat-item quiet" title="地图只绘制体积最大的节点，统计数字仍为全部结果">地图显示最大 <strong>${(state.truncation.total - state.truncation.omitted).toLocaleString('zh-CN')}</strong> 项，另有 <strong>${state.truncation.omitted.toLocaleString('zh-CN')}</strong> 项未绘制</span>` : '';
  $('#mapStats').innerHTML = `<span class="stat-item"><strong>${Number(count).toLocaleString('zh-CN')}</strong> 个文件</span><span class="stat-item"><strong>${formatBytes(bytes)}</strong> 已观察</span><span class="stat-item"><strong>${recs}</strong> 个建议</span>${omitted}`;
}

export function buildSidebar() {
  const sums={};state.allNodes.forEach(n=>sums[n.zone]=(sums[n.zone]||0)+n.size);
  $('#zoneList').innerHTML=Object.entries(sums).sort((a,b)=>b[1]-a[1]).map(([key,size])=>`<button class="zone-row" data-zone="${key}"><i style="background:${zones[key].color};color:${zones[key].color}"></i><span class="zone-name">${zones[key].name}</span><small class="zone-size">${formatBytes(size)}</small></button>`).join('') || '<p class="quiet">暂无区域数据</p>';
  const avg=state.allNodes.reduce((s,n)=>s+n.health,0)/(state.allNodes.length||1);$('#healthScore').textContent=Math.round(avg);
  const notes=[['giants','△','巨物正在升温','大型文件占据了显著空间'],['clones','∞','发现分身群落','相似副本正在形成聚落'],['endangered','◇','一座濒危岛','稀有文件值得额外关注']].filter(([z])=>state.allNodes.some(n=>n.zone===z));
  $('#briefCards').innerHTML=(notes.length?notes:[['active','○','生态状态平稳','暂未发现显著异常']]).slice(0,3).map(([z,s,t,p])=>`<article class="brief-card clickable" data-zone="${z}"><span class="card-symbol">${s}</span><div class="brief-copy"><strong>${t}</strong><p>${p}</p></div></article>`).join('');
}

export function search(query){const q=query.trim().toLowerCase(),box=$('#searchResults');if(!q){box.hidden=true;return;}const found=state.allNodes.filter(n=>`${n.name} ${n.path} ${zones[n.zone].name}`.toLowerCase().includes(q)).slice(0,7);box.innerHTML=found.length?found.map(n=>`<button class="search-result" data-node="${escapeHtml(n.id)}"><strong>${escapeHtml(n.name)}</strong><small class="result-path">${escapeHtml(n.path||zones[n.zone].name)} · ${formatBytes(n.size)}</small></button>`).join(''):'<div class="search-result"><small class="result-path">没有找到匹配的生态节点</small></div>';box.hidden=false;}
