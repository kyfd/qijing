// 整理与回收：候选清单 → 预览 → 逐项确认 → 移入 Windows 回收站。
// 界面文案只说“移入回收站 / 可还原”，不使用“安全删除”类措辞。
import { state } from '../state/state.js';
import { $, escapeHtml, toast, formatBytes } from '../components/dom.js';
import { adapter } from '../api/adapter.js';
import { loadMap } from '../map/nodes.js';

function renderRecycleCandidates() {
  const list = $('#recycleList'), items = state.recycle.candidates;
  if (!items.length) {
    list.innerHTML = '<p class="quiet">这次观察没有发现符合临时或残留特征的文件。</p>';
  } else {
    list.innerHTML = items.map((item, index) => {
      const checked = state.recycle.selected.has(item.entry_id) ? 'checked' : '';
      const blocked = item.eligible ? '' : 'blocked';
      const note = item.eligible ? escapeHtml(item.reason || '') : `无法回收：${escapeHtml(item.blocker || '未知原因')}`;
      return `<label class="recycle-row ${blocked}"><input type="checkbox" data-recycle-index="${index}" ${checked} ${item.eligible ? '' : 'disabled'}><span class="recycle-copy"><strong>${escapeHtml(item.name)}</strong><small class="recycle-path">${escapeHtml(item.path)}</small><small class="recycle-note">${note}</small></span><span class="recycle-size">${formatBytes(item.size)}</span></label>`;
    }).join('');
  }
  updateRecycleSummary();
}

function updateRecycleSummary() {
  const chosen = state.recycle.candidates.filter(item => state.recycle.selected.has(item.entry_id));
  const bytes = chosen.reduce((sum, item) => sum + Number(item.size || 0), 0);
  $('#recycleSummary').textContent = chosen.length ? `已选择 ${chosen.length} 项 · ${formatBytes(bytes)}` : '尚未选择任何文件';
  $('#recycleReviewBtn').disabled = !chosen.length || state.recycle.busy;
}

export async function openRecycle() {
  if (state.demo) { toast('演示生态中的文件并不存在，无法回收'); return; }
  $('#recycleDialog').showModal();
  $('#recycleList').innerHTML = '<p class="quiet">正在读取本地观察结果…</p>';
  state.recycle.selected = new Set();
  try {
    const data = await adapter.recycleCandidates();
    state.recycle.candidates = data.candidates || [];
    renderRecycleCandidates();
  } catch (error) {
    $('#recycleList').innerHTML = `<p class="quiet">无法读取候选清单：${escapeHtml(error.message || error)}</p>`;
  }
  loadRecycleHistory();
}

async function loadRecycleHistory() {
  try {
    const data = await adapter.recycleHistory();
    const items = data.items || [];
    $('#recycleHistory').innerHTML = items.length ? items.map(item => {
      const label = { recycled: '已移入回收站', refused: 'Windows 未接受', failed: '失败' }[item.outcome] || item.outcome;
      return `<div class="recycle-history-row"><strong>${escapeHtml(item.name)}</strong><span>${escapeHtml(label)}</span><small>${escapeHtml(String(item.recycled_at || '').slice(0, 19).replace('T', ' '))} · ${formatBytes(item.size)}</small><small class="recycle-path">${escapeHtml(item.path)}</small></div>`;
    }).join('') : '<p class="quiet">尚未回收过任何文件</p>';
  } catch (_) {}
}

export async function reviewRecycle() {
  const ids = state.recycle.candidates.filter(item => state.recycle.selected.has(item.entry_id)).map(item => item.entry_id);
  if (!ids.length) return;
  const button = $('#recycleReviewBtn');
  button.disabled = true;
  try {
    state.recycle.preview = await adapter.previewRecycle(ids);
    const items = state.recycle.preview.items || [];
    $('#recycleConfirmList').innerHTML = items.map(item => `<div class="recycle-confirm-row"><strong>${escapeHtml(item.name)}</strong><small class="recycle-path">${escapeHtml(item.path)}</small><span>${formatBytes(item.size)}</span></div>`).join('');
    $('#recycleConfirmSummary').textContent = `${items.length} 个文件 · 共 ${formatBytes(state.recycle.preview.total_bytes || 0)}`;
    $('#recycleConfirmHash').textContent = state.recycle.preview.selection_hash || '—';
    $('#recycleConfirmHash').title = state.recycle.preview.selection_hash || '';
    $('#recycleConfirmDialog').showModal();
  } catch (error) {
    toast(`无法生成回收预览：${error.message || error}`);
  } finally { button.disabled = false; }
}

export async function confirmRecycle() {
  const preview = state.recycle.preview;
  if (!preview) return;
  const button = $('#confirmRecycleBtn');
  button.disabled = true; button.textContent = '正在移入回收站…';
  state.recycle.busy = true;
  try {
    const result = await adapter.confirmRecycle(preview.selection_hash, preview.confirmation_token);
    $('#recycleConfirmDialog').close();
    state.recycle.preview = null;
    state.recycle.selected = new Set();
    const failed = result.failed || 0;
    toast(failed ? `${result.recycled} 项已移入回收站，${failed} 项未处理，可在回收站还原` : `${result.recycled} 项已移入回收站，可随时从 Windows 回收站还原`);
    if (failed) result.items.filter(item => item.error).forEach(item => toast(`${item.name}：${item.error}`));
    await openRecycle();
    await loadMap();
  } catch (error) {
    toast(`回收未执行：${error.message || '确认已失效，请重新选择'}`);
  } finally {
    state.recycle.busy = false;
    button.disabled = false; button.textContent = '确认移入回收站';
  }
}

export function onRecycleListChange(input) {
  const item = state.recycle.candidates[Number(input.dataset.recycleIndex)];
  if (!item) return;
  if (input.checked) state.recycle.selected.add(item.entry_id);
  else state.recycle.selected.delete(item.entry_id);
  updateRecycleSummary();
}
