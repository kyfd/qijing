// 演示生态：无后端时的本地样本，用于首次体验与界面验收。
import { state, demoNodes } from '../state/state.js';
import { toast } from '../components/dom.js';
import { adapter, desktopError } from '../api/adapter.js';
import { applyNodes } from './nodes.js';
import { normalizeNode } from './nodes.js';
import { renderEmpty, updateStats, buildSidebar } from './sidebar.js';
import { fitView } from './view.js';
import { draw } from './render.js';

export async function loadDemo(data) {
  const raw = data && (data.nodes || data.items);
  state.expanded.clear();
  state.truncation = null;
  await applyNodes((raw?.length ? raw : demoNodes).map(normalizeNode));
  state.demo = true; state.recommendations = [{node_id:'n3'},{node_id:'n6'},{node_id:'n11'}];
  document.querySelector('#lastScan').textContent = '演示生态 · 本地样本';
  document.querySelector('#connectionState').textContent = '演示模式';
  renderEmpty(false); updateStats(); buildSidebar(); fitView(); draw();
  toast('演示生态已苏醒，可拖拽并点击探索');
}

export async function startDemo() {
  try { loadDemo(await adapter.demo()); } catch (error) { if (adapter.mode === 'desktop') toast(desktopError('载入演示生态', error)); loadDemo(); }
}
