// 扫描相关控件的呈现：进度面板、开始/取消按钮、暂停按钮。
import { state } from '../state/state.js';
import { $, firstValue, formatCount, formatBytes, formatDuration } from '../components/dom.js';
import { isScanning, scanProgress } from './status.js';

export function renderScanProgress(status = {}, displayState) {
  const progress = scanProgress(status), nested = status.progress && typeof status.progress === 'object' ? status.progress : {}, scanning = displayState === 'degraded' || isScanning(status), cancelling = state.cancelling || ['cancelling','canceling'].includes(String(firstValue(nested.state, nested.status, status.state, status.status, '')).toLowerCase());
  const panel = $('#scanProgress'), track = panel.querySelector('[role="progressbar"]');
  let mode = displayState || (cancelling ? 'cancelling' : (scanning ? 'running' : (state.scanProgress.startedAt || status.last_scan ? 'complete' : 'idle')));
  let title = mode === 'degraded' ? '状态连接波动' : mode === 'cancelling' ? '正在停止扫描' : mode === 'running' ? (progress.phase ? `扫描 · ${progress.phase}` : '扫描进行中') : mode === 'complete' ? '最近扫描已结束' : '扫描待命';
  let root = mode === 'degraded' ? `暂时无法刷新，正在重试${progress.root ? ` · ${progress.root}` : ''}` : (progress.root || (mode === 'idle' ? '等待开始只读观察' : mode === 'complete' ? '当前显示最近一次只读快照' : '正在确认观察根目录'));
  panel.dataset.state = mode; panel.setAttribute('aria-busy', String(mode === 'running' || mode === 'cancelling' || mode === 'degraded')); $('#scanProgressTitle').textContent = title; $('#scanProgressRoot').textContent = root; $('#scanProgressRoot').title = root;
  $('#scanProgressFiles').textContent = formatCount(progress.files); $('#scanProgressDirs').textContent = formatCount(progress.dirs); $('#scanProgressBytes').textContent = formatBytes(progress.bytes); $('#scanProgressElapsed').textContent = formatDuration(progress.elapsed); $('#scanProgressBudget').textContent = progress.budget; $('#scanProgressBudget').title = progress.budget;
  track.removeAttribute('aria-valuenow'); track.removeAttribute('aria-valuemin'); track.removeAttribute('aria-valuemax');
  track.setAttribute('aria-valuetext', `${title}；${root}；${formatCount(progress.files)} 个文件，${formatCount(progress.dirs)} 个目录，${formatBytes(progress.bytes)}，耗时 ${formatDuration(progress.elapsed)}，预算 ${progress.budget}`);
  state.scanProgress.last = status;
}

export function setScanUI(scanning, cancelling = false) {
  state.scanning = scanning; state.cancelling = cancelling;
  const button = $('#scanBtn'), label = button.querySelector('span:last-child');
  button.classList.toggle('scanning', scanning); button.classList.toggle('cancelling', cancelling);
  button.setAttribute('aria-label', scanning ? '取消扫描' : '开始扫描');
  label.textContent = cancelling ? '正在取消…' : (scanning ? '取消扫描' : '开始扫描');
  updatePauseButton();
}

export function updatePauseButton() {
  const button = $('#pauseBtn'), label = button.querySelector('span:last-child');
  button.hidden = !state.scanning;
  button.classList.toggle('scanning', state.scanPaused);
  button.setAttribute('aria-label', state.scanPaused ? '继续扫描' : '暂停扫描');
  label.textContent = state.scanPaused ? '继续扫描' : '暂停扫描';
}
