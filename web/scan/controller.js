// 扫描任务生命周期：启动、取消、暂停/继续、状态轮询与启动引导。
import { state } from '../state/state.js';
import { $, toast } from '../components/dom.js';
import { adapter, desktopError } from '../api/adapter.js';
import { isScanning, scanError } from './status.js';
import { renderScanProgress, setScanUI, updatePauseButton } from './ui.js';
import { updateTrustStrip, loadModelProfile } from '../agent/profile.js';
import { loadRoots, loadDrives } from './roots.js';
import { loadMap } from '../map/nodes.js';
import { renderEmpty, updateStats } from '../map/sidebar.js';

export async function bootstrap() {
  try {
    const status = await adapter.status();
    state.token = status.token || status.bootstrap_token || status.csrf_token || '';
    applyStatus(status);
    await Promise.allSettled([loadRoots(), loadMap(), loadModelProfile(), loadDrives()]);
    if (isScanning(status)) beginStatusPolling();
  } catch (error) {
    $('#connectionState').textContent = adapter.mode === 'desktop' ? '桌面连接异常' : '本地待命';
    renderEmpty(true);
    if (adapter.mode === 'desktop') toast(desktopError('连接桌面服务', error));
  }
}

export function applyStatus(status = {}) {
  const scanning = isScanning(status);
  const paused = Boolean(status.progress?.paused || status.progress?.Paused) && scanning;
  if (state.scanPaused !== paused) { state.scanPaused = paused; updatePauseButton(); }
  if (scanning && !state.scanProgress.startedAt) state.scanProgress.startedAt = Date.now();
  setScanUI(scanning, state.cancelling && scanning);
  renderScanProgress(status);
  if (typeof status.network === 'boolean') state.model.network_enabled = status.network;
  updateTrustStrip();
  $('#connectionState').textContent = scanning ? (state.cancelling ? '正在停止观察' : '正在观察') : (adapter.mode === 'desktop' ? '桌面已连接' : '本地已连接');
  if (status.last_scan) $('#lastScan').textContent = `上次观察 ${status.last_scan}`;
  if (status.stats) updateStats(status.stats);
}

export function beginStatusPolling() {
  clearTimeout(state.statusTimer);
  state.statusTimer = window.setTimeout(pollStatus, 650);
}

export async function pollStatus() {
  try {
    const status = await adapter.status();
    const wasScanning = state.scanning;
    const responseTask = String(status.scan_id || '');
    if (state.scanProgress.taskId && responseTask && responseTask !== state.scanProgress.taskId) {
      return beginStatusPolling();
    }
    if (!state.scanProgress.taskId && isScanning(status) && responseTask) state.scanProgress.taskId = responseTask;
    state.scanProgress.failures = 0;
    applyStatus(status);
    const error = scanError(status);
    if (error) toast(adapter.mode === 'desktop' ? desktopError('扫描', error) : `扫描失败：${error}`);
    renderScanProgress(status);
    if (isScanning(status)) return beginStatusPolling();
    state.cancelling = false;
    if (wasScanning) {
      state.scanProgress.startedAt = 0;
      state.scanProgress.taskId = '';
      await loadMap();
      const taskResult = String(status.task_result || '').toLowerCase();
      const terminalReason = taskResult === 'cancelled' ? 'cancelled' : status.truncation_reason;
      if (status.truncated || status.partial || taskResult === 'cancelled') {
        const reasons={entry_limit:'达到 50 万条目预算',error_limit:'达到错误预算',duration_limit:'达到 30 分钟预算',cancelled:'已取消'};
        toast(`只读观察已停止：${reasons[terminalReason]||'结果为部分快照'}，原文件未受影响`);
      } else toast(error ? '扫描已结束，请查看错误信息' : '只读观察已完成');
    }
  } catch (error) {
    state.scanProgress.failures += 1;
    const retryDelay = Math.min(5000, 650 * Math.pow(1.7, state.scanProgress.failures));
    renderScanProgress(state.scanProgress.last || {}, 'degraded');
    $('#connectionState').textContent = '状态连接波动';
    if (state.scanProgress.failures === 1 || state.scanProgress.failures % 5 === 0) toast(adapter.mode === 'desktop' ? desktopError('读取扫描状态', error) : '扫描状态暂时不可用，正在自动重试');
    clearTimeout(state.statusTimer);
    state.statusTimer = window.setTimeout(pollStatus, retryDelay);
  }
}

export async function startScan() {
  if (state.scanning) return cancelScan();
  if (!state.roots.length) { toast('请先授权至少一个观察根目录'); $('#settingsDialog').showModal(); return; }
  state.demo = false; state.scanProgress.startedAt = Date.now(); state.scanProgress.failures = 0; setScanUI(true); renderScanProgress({ scanning: true, progress: { phase: 'starting' } }); $('#connectionState').textContent = '正在观察';
  try {
    Promise.resolve(adapter.startScan()).then(result => { state.scanProgress.taskId = String(result?.scan_id || state.scanProgress.taskId || ''); beginStatusPolling(); }).catch(error => {
      if (error?.name === 'AbortError' && state.cancelling) { beginStatusPolling(); return; }
      setScanUI(false);
      toast(adapter.mode === 'desktop' ? desktopError('开始扫描', error) : `开始扫描失败：${error.message || '本地服务未响应'}`);
    });
    toast('只读观察任务已开始，再次点击可取消');
    beginStatusPolling();
  } catch (error) {
    setScanUI(false);
    toast(adapter.mode === 'desktop' ? desktopError('开始扫描', error) : '本地服务尚未响应，请确认 Agent 已启动');
  }
}

export async function cancelScan() {
  if (state.cancelling) return;
  state.cancelling = true; setScanUI(true, true); renderScanProgress(state.scanProgress.last || { scanning: true }, 'cancelling');
  try {
    await adapter.cancelScan();
    toast('已请求取消，正在等待当前只读操作停止');
    beginStatusPolling();
  } catch (error) {
    state.cancelling = false; setScanUI(true);
    const message = adapter.mode === 'desktop' ? desktopError('取消扫描', error) : `取消扫描失败：${error.message || '当前 HTTP 服务不支持取消'}`;
    toast(message);
    beginStatusPolling();
  }
}

export async function togglePause() {
  if (!state.scanning || state.cancelling) return;
  try {
    if (state.scanPaused) {
      await adapter.resumeScan();
      state.scanPaused = false;
      toast('已继续扫描');
    } else {
      await adapter.pauseScan();
      state.scanPaused = true;
      toast('已暂停扫描，扫描进程保持等待');
    }
    updatePauseButton();
  } catch (error) {
    toast(adapter.mode === 'desktop' ? desktopError('暂停/继续扫描', error) : `操作失败：${error.message || '本地服务未响应'}`);
    beginStatusPolling();
  }
}
