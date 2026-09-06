// 扫描状态解析：把后端多代字段名归一成一个稳定的进度视图。
// 纯函数，无 DOM 访问，便于针对字段变体补测试。
import { state } from '../state/state.js';
import { firstValue, numeric, formatCount, formatDuration } from '../components/dom.js';

export function isScanning(status = {}) { const progress = status.progress && typeof status.progress === 'object' ? status.progress : {}; return Boolean(status.scanning ?? status.running ?? progress.scanning ?? progress.running ?? ['running','scanning','cancelling','canceling'].includes(String(firstValue(progress.state, progress.status, status.state, status.status, '')).toLowerCase())); }

export function scanError(status = {}) { const progress = status.progress && typeof status.progress === 'object' ? status.progress : {}; return progress.error || progress.last_error || status.error || status.last_error || status.scan_error || ''; }

export function scanProgress(status = {}) {
  const progress = status.progress && typeof status.progress === 'object' ? status.progress : {};
  const counters = progress.counters || progress.stats || status.scan_stats || status.stats || {};
  const budget = progress.budget || progress.budgets || status.budget || status.budgets || {};
  const roots = progress.roots || progress.root_progress || [];
  const activeRootValue = firstValue(progress.current_root_label, progress.current_root, progress.current_root_path, progress.root, progress.root_path, status.current_root, status.scan_root, Array.isArray(roots) ? roots.find(root => ['running','scanning','active'].includes(String(root?.state || root?.status).toLowerCase())) : '');
  const activeRoot = typeof activeRootValue === 'object' ? firstValue(activeRootValue?.path, activeRootValue?.root, activeRootValue?.name, '') : activeRootValue;
  const rawPhase = String(firstValue(progress.phase, progress.stage, status.phase, status.scan_phase, '')).toLowerCase();
  const phases = {queued:'排队',starting:'准备',preparing:'准备',walking:'遍历目录',traversing:'遍历目录',discovering:'发现文件',scanning:'扫描元数据',indexing:'建立索引',classifying:'分析生态',analyzing:'分析生态',relations:'分析关系',saving:'保存快照',finalizing:'整理快照',complete:'完成',completed:'完成',cancelling:'停止中',cancelled:'已取消',canceled:'已取消'};
  const started = firstValue(progress.started_at, progress.startedAt, status.scan_started_at, status.started_at);
  const parsedStarted = started ? Date.parse(started) : NaN;
  const elapsedRaw = firstValue(progress.elapsed_ms, progress.duration_ms, status.elapsed_ms, status.scan_duration_ms);
  const elapsed = elapsedRaw != null ? numeric(elapsedRaw) : (Number.isFinite(parsedStarted) ? Date.now() - parsedStarted : (state.scanProgress.startedAt ? Date.now() - state.scanProgress.startedAt : NaN));
  const entryBudget = budget.entry_budget || progress.entry_budget || {};
  const errorBudget = budget.error_budget || progress.error_budget || {};
  const durationBudget = budget.duration_budget_ms || progress.duration_budget_ms || {};
  const limit = numeric(entryBudget.limit, budget.entry_limit, budget.max_entries, budget.file_limit, progress.entry_limit, status.entry_limit);
  const errors = numeric(errorBudget.used, counters.errors, counters.error_count, progress.errors, status.error_count);
  const errorLimit = numeric(errorBudget.limit, budget.error_limit, budget.max_errors, progress.error_limit, status.error_limit);
  const durationLimit = numeric(durationBudget.limit, budget.duration_ms, budget.max_duration_ms, progress.duration_limit_ms, status.duration_limit_ms);
  const files = numeric(counters.files, counters.file_count, counters.files_scanned, progress.files, progress.files_scanned, status.files_scanned);
  const dirs = numeric(counters.directories, counters.dirs, counters.directory_count, counters.directories_scanned, progress.directories, progress.dirs, status.directories_scanned);
  const bytes = numeric(counters.bytes, counters.total_bytes, counters.bytes_scanned, progress.bytes, progress.bytes_scanned, status.bytes_scanned);
  const observedEntries = numeric(entryBudget.used, progress.observed_entries, counters.observed_entries, counters.entries, files + dirs);
  const budgetParts = [];
  if (limit) budgetParts.push(`${formatCount(observedEntries)}/${formatCount(limit)} 条目`);
  if (errorLimit) budgetParts.push(`${formatCount(errors)}/${formatCount(errorLimit)} 错误`);
  if (durationLimit) budgetParts.push(`${formatDuration(elapsed)}/${formatDuration(durationLimit)}`);
  const rootsCompleted = numeric(progress.roots_completed, progress.completed_roots);
  const rootsTotal = numeric(progress.roots_total, progress.total_roots);
  const rootPrefix = rootsTotal ? `${formatCount(rootsCompleted)} / ${formatCount(rootsTotal)} 个根目录` : '';
  return { phase: phases[rawPhase] || (rawPhase ? rawPhase : ''), root: [rootPrefix, activeRoot || ''].filter(Boolean).join(' · '), files, dirs, bytes, elapsed, budget: budgetParts.join(' · ') || '未报告限制' };
}
