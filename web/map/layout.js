// 布局编排：优先走 Worker，构造失败（无 Worker 环境或安全策略限制）时
// 退回主线程同步计算，两条路径共用同一份 layoutCore，结果完全一致。
import { layoutNodes } from './layoutCore.js';

let worker = null;
try {
  worker = new Worker(new URL('../workers/layout.worker.js', import.meta.url), { type: 'module' });
} catch (_) {
  worker = null;
}

let seq = 0;
const pending = new Map();

if (worker) {
  worker.onmessage = (e) => {
    const { id, nodes, error } = e.data || {};
    const resolve = pending.get(id);
    if (!resolve) return;
    pending.delete(id);
    resolve({ nodes, error: error || null });
  };
  worker.onerror = () => {
    // 脚本级错误（如模块加载失败）会让所有等待者永远挂起——一次性
    // 降级到主线程，并对每个等待中的请求返回失败标记。
    for (const resolve of pending.values()) resolve({ nodes: null, error: 'worker failed' });
    pending.clear();
    worker = null;
  };
}

// stageAspect：布局的横向拉伸需要舞台宽高比；Worker 里拿不到 DOM，
// 由主线程量好再传进去。
export function stageAspect(stage) {
  return Math.max(1, (stage.clientWidth || 1) / Math.max(1, stage.clientHeight || 1));
}

export function runLayout(nodes, aspect) {
  if (!worker) {
    layoutNodes(nodes, aspect);
    return Promise.resolve({ nodes, error: null });
  }
  return new Promise((resolve) => {
    const id = ++seq;
    pending.set(id, resolve);
    worker.postMessage({ id, nodes, aspect });
  });
}
