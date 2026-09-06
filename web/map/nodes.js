// 节点管道：归一化后端数据 → 聚合长尾 → 布局（Worker）→ 渲染。
import { state } from '../state/state.js';
import { zones } from '../state/state.js';
import { adapter } from '../api/adapter.js';
import { runLayout, stageAspect } from './layout.js';
import { stage, draw } from './render.js';
import { fitView } from './view.js';
import { renderEmpty, updateStats, buildSidebar } from './sidebar.js';

export function normalizeNode(node, index) {
  const zoneKey = node.zone || node.region || node.category || 'active';
  return { id: node.id ?? String(index), name: node.name || node.label || '未命名节点', path: node.path || '', size: Number(node.size ?? node.bytes ?? 0), health: Number(node.health ?? node.score ?? 60), zone: zones[zoneKey] ? zoneKey : 'active', x: 0, y: 0, r: 0, modified: node.modified || node.last_modified || '未知', kind: node.kind || node.type || '文件' };
}

// A real drive has a long tail of tiny files. Drawn individually they become
// hundreds of identical specks — visually noisy and, packed together, an
// unpleasant clustered texture. Collapse each zone's tail into one node the
// user can open and expand, so nothing is hidden but the map stays readable.
const AGGREGATE_MIN_MEMBERS = 6;

export function aggregateNodes(all) {
  if (!all.length) return all;
  const largest = Math.max(...all.map(n => n.size), 1);
  const threshold = largest * 0.005;
  const groups = new Map();
  for (const node of all) {
    if (!groups.has(node.zone)) groups.set(node.zone, []);
    groups.get(node.zone).push(node);
  }
  const out = [];
  for (const [zone, members] of groups) {
    const key = `agg:${zone}`;
    const small = members.filter(n => n.size < threshold);
    if (small.length < AGGREGATE_MIN_MEMBERS || state.expanded.has(key)) { out.push(...members); continue; }
    out.push(...members.filter(n => n.size >= threshold));
    const bytes = small.reduce((s, n) => s + n.size, 0);
    out.push({
      id: key, aggregate: true, members: small,
      name: `${small.length} 个小文件`, path: '',
      size: bytes, health: small.reduce((s, n) => s + n.health, 0) / small.length,
      zone, x: 0, y: 0, r: 0, modified: '—', kind: '聚合'
    });
  }
  return out;
}

// 布局在 Worker 里跑：结构化克隆意味着 Worker 改的是自己的副本，
// 所以坐标算完后要按位拷回主线程的同一批对象——搜索与聚合展开依赖
// 对象身份不变。计算失败时退回主线程，两条路径都不会丢节点。
async function layoutCurrentNodes() {
  const { nodes, error } = await runLayout(state.nodes, stageAspect(stage));
  if (error) { console.warn('worker layout failed:', error); return; }
  for (let i = 0; i < state.nodes.length; i++) {
    const laid = nodes[i];
    if (!laid) break;
    const target = state.nodes[i];
    target.x = laid.x; target.y = laid.y; target.r = laid.r;
    if (laid.pad !== undefined) target.pad = laid.pad;
  }
}

export async function applyNodes(all) {
  state.allNodes = all;
  state.nodes = aggregateNodes(all);
  await layoutCurrentNodes();
}

export async function expandAggregate(key) {
  state.expanded.add(key);
  state.nodes = aggregateNodes(state.allNodes);
  await layoutCurrentNodes();
  fitView();
  draw();
}

// loadMap 拉取当前快照的地图视图。地图只携带后端挑选的最大条目；
// 截断信息如实交给统计条展示，绝不暗示“这就是全部文件”。
export async function loadMap() {
  const data = await adapter.map();
  const raw = Array.isArray(data) ? data : (data.nodes || data.items || []);
  state.expanded.clear();
  await applyNodes(raw.map(normalizeNode));
  state.recommendations = data.recommendations || [];
  state.truncation = data.nodes_truncated ? { omitted: data.nodes_omitted || 0, total: data.nodes_total || 0 } : null;
  renderEmpty(!state.nodes.length);
  updateStats(data.stats || {});
  buildSidebar();
  fitView();
  draw();
}
