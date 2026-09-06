// 传输层：桌面版走 Wails 原生桥接，开发预览走本地 HTTP API。
// 两个通道暴露同一组方法，业务模块不感知差异。
import { state } from '../state/state.js';

export function decodeResult(value) {
  if (typeof value !== 'string') return value ?? {};
  const text = value.trim();
  if (!text) return {};
  try { return JSON.parse(text); } catch (_) { return value; }
}

function nativeBridge() {
  const candidates = [window.go?.desktop?.NativeBridge, window.go?.desktop?.Bridge, window.ecosystemNative, window.nativeBridge];
  return candidates.find(binding => typeof desktopMethod(binding, 'ChooseDirectory') === 'function') || null;
}

function desktopMethod(binding, name) {
  return binding?.[name] || binding?.[name.charAt(0).toLowerCase() + name.slice(1)];
}

async function invokeNative(name, ...args) {
  const binding = nativeBridge(), method = desktopMethod(binding, name);
  if (typeof method !== 'function') throw new Error(`桌面原生桥接缺少 ${name} 方法`);
  return decodeResult(await method.call(binding, ...args));
}

function makeHttpAdapter() {
  let scanController = null;
  async function request(path, options = {}) {
    const method = options.method || 'GET';
    const headers = { 'Accept': 'application/json', ...(options.headers || {}) };
    if (method !== 'GET' && state.token) headers['X-Ecosystem-Token'] = state.token;
    const body = options.body && typeof options.body !== 'string' ? JSON.stringify(options.body) : options.body;
    if (body) headers['Content-Type'] = 'application/json';
    const response = await fetch(`/api/v1${path}`, { ...options, body, method, headers });
    const text = await response.text();
    if (!response.ok) throw new Error(text.trim() || `${response.status} ${response.statusText}`);
    return text ? decodeResult(text) : {};
  }
  return {
    mode: nativeBridge() ? 'desktop' : 'http',
    status: () => request('/status'),
    listRoots: () => request('/roots'),
    chooseDirectory: nativeBridge() ? () => invokeNative('ChooseDirectory') : null,
    listLocalDrives: nativeBridge() ? () => invokeNative('ListLocalDrives') : null,
    addRoot: (path) => request('/roots', { method: 'POST', body: { path } }),
    addRootBatch: (paths, start_scan = true) => request('/roots/batch', { method: 'POST', body: { paths, start_scan } }),
    removeRoot: (path) => request('/roots', { method: 'DELETE', body: { path } }),
    startScan: () => {
      scanController = new AbortController();
      return request('/scan', { method: 'POST', body: { roots: state.roots.map(r => r.path || r) }, signal: scanController.signal }).finally(() => { scanController = null; });
    },
    cancelScan: async () => {
      const result = await request('/scan/cancel', { method: 'POST' });
      if (scanController) scanController.abort();
      return result;
    },
    pauseScan: () => request('/scan/pause', { method: 'POST' }),
    resumeScan: () => request('/scan/resume', { method: 'POST' }),
    map: () => request('/map'),
    node: (id) => request(`/nodes/${encodeURIComponent(id)}`),
    revealNode: (id) => request(`/nodes/${encodeURIComponent(id)}/reveal`, { method: 'POST' }),
    ignoreRecommendation: (id) => request(`/recommendations/${encodeURIComponent(id)}/ignore`, { method: 'POST' }),
    privacy: () => request('/privacy'),
    recycleCandidates: () => request('/recycle/candidates'),
    previewRecycle: (entry_ids) => request('/recycle/preview', { method: 'POST', body: { entry_ids } }),
    confirmRecycle: (selection_hash, confirmation_token) => request('/recycle/confirm', { method: 'POST', body: { selection_hash, confirmation_token } }),
    recycleHistory: () => request('/recycle/history'),
    demo: () => request('/demo', { method: 'POST' }),
    getModelProfile: () => request('/model/profile'),
    saveModelProfile: (profile) => request('/model/profile', { method: 'PUT', body: profile }),
    setAPIKey: (api_key) => request('/model/key', { method: 'PUT', body: { api_key } }),
    testModelConnection: () => request('/model/test', { method: 'POST' }),
    setNetworkEnabled: (enabled) => request('/model/network', { method: 'PUT', body: { enabled } }),
    previewAgentRun: () => request('/agent/preview', { method: 'POST' }),
    startAgentRun: (hash, token) => request('/agent/runs', { method: 'POST', body: { payload_hash: hash, confirmation_token: token } }),
    cancelAgentRun: (id) => request(`/agent/runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
    agentRunStatus: (id) => request(`/agent/runs/${encodeURIComponent(id)}`),
    agentRunResult: (id) => request(`/agent/runs/${encodeURIComponent(id)}/result`),
    listAgentAudits: (id) => id ? request(`/agent/runs/${encodeURIComponent(id)}/audits`) : Promise.resolve({ steps: [] })
  };
}

export const adapter = makeHttpAdapter();

export const desktopError = (action, error) => `${action}失败：${error?.message || error || '桌面服务未响应'}`;
