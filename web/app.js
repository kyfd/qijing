(() => {
  'use strict';

  const $ = (selector) => document.querySelector(selector);
  const canvas = $('#ecosystemCanvas');
  const stage = $('#canvasStage');
  const ctx = canvas.getContext('2d');
  const state = { token: '', nodes: [], allNodes: [], expanded: new Set(), roots: [], drives: [], recommendations: [], view: { x: 0, y: 0, scale: 1 }, dragging: false, moved: false, last: null, hover: null, demo: false, scanning: false, cancelling: false, statusTimer: 0, scanProgress: { startedAt: 0, failures: 0, last: null, taskId: '' }, model: { provider_type: 'cloud', base_url: '', model: '', network_enabled: false, has_api_key: false }, agent: { preview: null, runId: '', running: false, timer: 0, startedAt: 0, lastPayload: null, audits: [] }, recycle: { candidates: [], selected: new Set(), preview: null, busy: false } };
  const zones = {
    active: { name: '活跃森林', color: '#6fd48c', description: '近期频繁生长与访问的文件' },
    seedlings: { name: '幼苗区', color: '#c6e56a', description: '新近出现、仍在成长的文件' },
    downloads: { name: '下载荒原', color: '#e0ad5a', description: '下载后鲜少再被访问的沉积' },
    zombies: { name: '僵尸墓地', color: '#8f9d96', description: '长久沉睡但仍占据空间的文件' },
    giants: { name: '巨物火山', color: '#e07a52', description: '占用显著的超大型文件' },
    clones: { name: '分身群落', color: '#7eb8d0', description: '内容相同或高度相似的副本' },
    decay: { name: '腐烂区', color: '#c48a62', description: '临时、缓存或可能已失效的文件' },
    endangered: { name: '濒危岛', color: '#c9a0d4', description: '稀有、孤立且值得备份的文件' }
  };
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)');
  const demoNodes = [
    ['n1','设计资产库','D:\\Studio\\Assets',36.2e9,'active',84,-240,-85,112],
    ['n2','年度影像','D:\\Photos\\2025',62.8e9,'giants',48,140,-72,138],
    ['n3','浏览器下载','C:\\Users\\Me\\Downloads',18.4e9,'downloads',39,-90,150,86],
    ['n4','项目副本','D:\\Work\\Archive',12.1e9,'clones',55,226,152,72],
    ['n5','新建项目','D:\\Work\\Sprout',4.8e9,'seedlings',91,-308,164,54],
    ['n6','旧版安装包','D:\\Software\\Legacy',9.6e9,'zombies',28,342,-125,65],
    ['n7','构建缓存','D:\\Work\\.cache',6.7e9,'decay',31,42,276,58],
    ['n8','家族录音','D:\\Memories\\Audio',3.2e9,'endangered',74,-405,-215,48],
    ['n9','品牌手册.pdf','D:\\Studio\\品牌手册.pdf',880e6,'active',96,-135,-175,28],
    ['n10','航拍原片.mov','D:\\Photos\\航拍原片.mov',21.5e9,'giants',44,101,-102,62],
    ['n11','invoice-final (2).pdf','D:\\Downloads\\invoice-final (2).pdf',24e6,'clones',42,205,194,22],
    ['n12','未命名文件夹','D:\\Downloads\\未命名文件夹',1.4e9,'downloads',36,-39,190,34]
  ].map(([id,name,path,size,zone,health,x,y,r]) => ({ id,name,path,size,zone,health,x,y,r,modified:'2026-08-09',kind:name.includes('.')?'文件':'文件夹' }));

  function formatBytes(bytes = 0) {
    if (!bytes) return '0 B';
    const units = ['B','KB','MB','GB','TB']; let i = 0, value = Number(bytes);
    while (value >= 1024 && i < units.length - 1) { value /= 1024; i++; }
    return `${value >= 10 || i === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[i]}`;
  }
  function normalizeNode(node, index) {
    const zoneKey = node.zone || node.region || node.category || 'active';
    return { id: node.id ?? String(index), name: node.name || node.label || '未命名节点', path: node.path || '', size: Number(node.size ?? node.bytes ?? 0), health: Number(node.health ?? node.score ?? 60), zone: zones[zoneKey] ? zoneKey : 'active', x: 0, y: 0, r: 0, modified: node.modified || node.last_modified || '未知', kind: node.kind || node.type || '文件' };
  }
  // A real drive has a long tail of tiny files. Drawn individually they become
  // hundreds of identical specks — visually noisy and, packed together, an
  // unpleasant clustered texture. Collapse each zone's tail into one node the
  // user can open and expand, so nothing is hidden but the map stays readable.
  const AGGREGATE_MIN_MEMBERS = 6;
  function aggregateNodes(all) {
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
  function applyNodes(all) {
    state.allNodes = all;
    state.nodes = aggregateNodes(all);
    layoutNodes(state.nodes);
  }
  function expandAggregate(key) {
    state.expanded.add(key);
    state.nodes = aggregateNodes(state.allNodes);
    layoutNodes(state.nodes);
    fitView();
    draw();
  }
  // Sizes are areas, not radii: a file twice as large must look twice as big.
  // The old formula bottomed out at a floor for anything under ~100 MB, which
  // flattened every file into the same bead.
  function layoutNodes(nodes) {
    if (!nodes.length) return;
    const largest = Math.max(...nodes.map(n => n.size), 1);
    const smallest = Math.max(1, Math.min(...nodes.map(n => Math.max(n.size, 1))));
    // Real drives span six or more orders of magnitude, so a pure power curve
    // pins everything under ~1% of the largest file to the floor and produces a
    // field of identical dots. Blend the area-proportional curve with a log
    // curve: area still reads as "bigger means heavier", while the log term
    // keeps small files distinguishable from each other.
    const logSpan = Math.log(largest / smallest) || 1;
    for (const node of nodes) {
      const size = Math.max(node.size, 1);
      const area = Math.pow(size / largest, 0.42);
      const logged = Math.log(size / smallest) / logSpan;
      node.r = 7 + 62 * (area * 0.45 + logged * 0.55);
    }
    const groups = new Map();
    for (const node of nodes) {
      if (!groups.has(node.zone)) groups.set(node.zone, []);
      groups.get(node.zone).push(node);
    }
    // Lay each zone out as its own cluster, then place the clusters around the
    // origin by weight so heavy regions sit near the centre of the map.
    const clusters = [...groups.entries()].map(([zone, members]) => {
      members.sort((a, b) => b.r - a.r);
      packCluster(members);
      const radius = Math.max(...members.map(m => Math.hypot(m.x, m.y) + m.r), 1);
      return { zone, members, radius, weight: members.reduce((s, m) => s + m.size, 0) };
    }).sort((a, b) => b.weight - a.weight);
    placeClusters(clusters);
    for (const cluster of clusters) {
      for (const member of cluster.members) { member.x += cluster.cx; member.y += cluster.cy; }
    }
    // Recentre on the bounding box so fitView can actually fill the stage
    // instead of leaving the whole field drifting into one corner.
    const minX = Math.min(...nodes.map(n => n.x - n.r)), maxX = Math.max(...nodes.map(n => n.x + n.r));
    const minY = Math.min(...nodes.map(n => n.y - n.r)), maxY = Math.max(...nodes.map(n => n.y + n.r));
    const cx = (minX + maxX) / 2, cy = (minY + maxY) / 2;
    for (const node of nodes) { node.x -= cx; node.y -= cy; }
    // The stage is a wide letterbox but a packed field is round, so a circular
    // layout wastes the sides. Stretch toward the viewport aspect, then
    // recentre again so the result sits squarely in the frame.
    const aspect = Math.max(1, (stage.clientWidth || 1) / Math.max(1, stage.clientHeight || 1));
    if (aspect > 1.25 && nodes.length > 1) {
      const stretch = Math.min(2.1, aspect / 1.2);
      for (const node of nodes) node.x *= stretch;
      const x0 = Math.min(...nodes.map(n => n.x - n.r)), x1 = Math.max(...nodes.map(n => n.x + n.r));
      const y0 = Math.min(...nodes.map(n => n.y - n.r)), y1 = Math.max(...nodes.map(n => n.y + n.r));
      const dx = (x0 + x1) / 2, dy = (y0 + y1) / 2;
      for (const node of nodes) { node.x -= dx; node.y -= dy; }
    }
  }
  // Seed each circle on a golden-angle spiral sized to the cluster's total
  // area, then relax overlaps. Pure spiral placement degenerates into a
  // hexagonal lattice when many circles share a radius; relaxation keeps
  // equal-sized files looking like a natural clump.
  //
  // Spacing is deliberately loose and uneven. A uniform minimum gap packs
  // equal-sized circles into a dense honeycomb blob, which reads as unpleasant
  // clustered-hole texture rather than as a living map. Per-node breathing room
  // scales with radius and carries a stable jitter so no two gaps match.
  function packCluster(members) {
    const area = members.reduce((sum, n) => sum + n.r * n.r, 0);
    const spread = Math.sqrt(area) * 2.4;
    members.forEach((node, i) => {
      const a = i * 2.399963;
      const d = spread * Math.sqrt((i + 0.5) / members.length);
      // Jitter proportional to the ring radius breaks the spiral's regularity
      // at the rim, where a lattice is most visible.
      const wobble = 0.35 + hash01(i * 7 + 3) * 0.5;
      node.x = Math.cos(a) * d + (hash01(i * 5 + 2) - 0.5) * (node.r + d * 0.18) * wobble;
      node.y = Math.sin(a) * d + (hash01(i * 5 + 8) - 0.5) * (node.r + d * 0.18) * wobble;
      node.pad = 9 + node.r * 0.55 + hash01(i * 11 + 5) * 14;
    });
    for (let pass = 0; pass < 90; pass++) {
      let moved = false;
      for (let i = 0; i < members.length; i++) {
        const a = members[i];
        for (let j = i + 1; j < members.length; j++) {
          const b = members[j];
          const dx = b.x - a.x, dy = b.y - a.y;
          const need = a.r + b.r + (a.pad + b.pad) / 2;
          let dist = Math.hypot(dx, dy);
          if (dist >= need) continue;
          if (dist < 0.01) { b.x += 0.4; b.y += 0.3; dist = 0.5; }
          const push = (need - dist) / 2;
          const ux = dx / dist * push, uy = dy / dist * push;
          a.x -= ux; a.y -= uy; b.x += ux; b.y += uy;
          moved = true;
        }
      }
      // Very light centre pull. Anything stronger re-compacts the clump into
      // the even-spaced blob the padding above exists to avoid.
      for (const node of members) { node.x *= 0.9993; node.y *= 0.9993; }
      if (!moved) break;
    }
  }
  function placeClusters(clusters) {
    const placed = [];
    for (let index = 0; index < clusters.length; index++) {
      const cluster = clusters[index];
      if (!placed.length) { cluster.cx = 0; cluster.cy = 0; placed.push(cluster); continue; }
      let best = null;
      for (let ring = 1; ring < 400 && !best; ring++) {
        const dist = ring * 5;
        const steps = Math.max(20, Math.floor(dist / 5));
        const offset = hash01(index * 29 + ring) * Math.PI * 2;
        for (let i = 0; i < steps; i++) {
          const a = (i / steps) * Math.PI * 2 + offset;
          const x = Math.cos(a) * dist, y = Math.sin(a) * dist;
          if (placed.every(p => Math.hypot(x - p.cx, y - p.cy) >= p.radius + cluster.radius + 54)) { best = { x, y }; break; }
        }
      }
      cluster.cx = best ? best.x : 0;
      cluster.cy = best ? best.y : 0;
      placed.push(cluster);
    }
  }
  function decodeResult(value) {
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
  const adapter = makeHttpAdapter();
  const desktopError = (action, error) => `${action}失败：${error?.message || error || '桌面服务未响应'}`;
  function isScanning(status = {}) { const progress = status.progress && typeof status.progress === 'object' ? status.progress : {}; return Boolean(status.scanning ?? status.running ?? progress.scanning ?? progress.running ?? ['running','scanning','cancelling','canceling'].includes(String(firstValue(progress.state, progress.status, status.state, status.status, '')).toLowerCase())); }
  function scanError(status = {}) { const progress = status.progress && typeof status.progress === 'object' ? status.progress : {}; return progress.error || progress.last_error || status.error || status.last_error || status.scan_error || ''; }
  function firstValue(...values) { return values.find(value => value !== undefined && value !== null && value !== ''); }
  function numeric(...values) { const value = Number(firstValue(...values)); return Number.isFinite(value) && value >= 0 ? value : 0; }
  function formatCount(value) { return Math.floor(numeric(value)).toLocaleString('zh-CN'); }
  function formatDuration(ms) {
    if (!Number.isFinite(ms) || ms < 0) return '—';
    const seconds = Math.floor(ms / 1000), hours = Math.floor(seconds / 3600), minutes = Math.floor((seconds % 3600) / 60), rest = seconds % 60;
    return hours ? `${hours}:${String(minutes).padStart(2,'0')}:${String(rest).padStart(2,'0')}` : `${minutes}:${String(rest).padStart(2,'0')}`;
  }
  function scanProgress(status = {}) {
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
  function renderScanProgress(status = {}, displayState) {
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
  function setScanUI(scanning, cancelling = false) {
    state.scanning = scanning; state.cancelling = cancelling;
    const button = $('#scanBtn'), label = button.querySelector('span:last-child');
    button.classList.toggle('scanning', scanning); button.classList.toggle('cancelling', cancelling);
    button.setAttribute('aria-label', scanning ? '取消扫描' : '开始扫描');
    label.textContent = cancelling ? '正在取消…' : (scanning ? '取消扫描' : '开始扫描');
  }
  async function bootstrap() {
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
  function applyStatus(status = {}) {
    const scanning = isScanning(status);
    if (scanning && !state.scanProgress.startedAt) state.scanProgress.startedAt = Date.now();
    setScanUI(scanning, state.cancelling && scanning);
    renderScanProgress(status);
    if (typeof status.network === 'boolean') state.model.network_enabled = status.network;
    updateTrustStrip();
    $('#connectionState').textContent = scanning ? (state.cancelling ? '正在停止观察' : '正在观察') : (adapter.mode === 'desktop' ? '桌面已连接' : '本地已连接');
    if (status.last_scan) $('#lastScan').textContent = `上次观察 ${status.last_scan}`;
    if (status.stats) updateStats(status.stats);
  }
  function normalizeProfile(data = {}) {
    const profile = data.profile || data;
    return { provider_type: profile.provider || profile.provider_type || profile.type || 'cloud', base_url: profile.base_url || profile.baseURL || '', model: profile.model || profile.model_name || '', network_enabled: Boolean(profile.network_enabled ?? profile.network), has_api_key: Boolean(profile.has_api_key ?? profile.api_key_configured) };
  }
  function updateTrustStrip() {
    const local = state.model.provider_type === 'local';
    $('#networkTrust').innerHTML = `<i class="cloud-mini"></i>${state.model.network_enabled ? '模型联网开启' : '模型联网关闭'}`;
    $('#networkTrust').classList.toggle('online', Boolean(state.model.network_enabled));
    $('#providerTrust').textContent = local ? '本地模型 · 数据不离机' : (state.model.model ? `云端模型 · ${state.model.model}` : '云端模型未配置');
    $('.agent-note p').innerHTML = state.model.network_enabled && !local ? '<strong>云端 Agent 已由你启用</strong><br>仅在逐次确认后发送匿名 payload。' : (local ? '<strong>本地模型待命</strong><br>Payload 仍会在运行前由你确认。' : '<strong>Agent 正在本地待命</strong><br>联网关闭，不会向外部模型发送数据。');
  }
  async function loadModelProfile() {
    try { state.model = normalizeProfile(await adapter.getModelProfile()); } catch (_) { state.model = normalizeProfile(state.model); }
    $('#agentBaseUrl').value = state.model.base_url;
    $('#agentModel').value = state.model.model;
    $('#networkEnabled').checked = state.model.network_enabled;
    const radio = document.querySelector(`input[name="providerType"][value="${state.model.provider_type}"]`);
    if (radio) radio.checked = true;
    $('#agentApiKey').placeholder = state.model.has_api_key ? '已安全保存 · 留空保持不变' : 'sk-••••••••';
    updateTrustStrip();
  }
  async function loadRoots() {
    const data = await adapter.listRoots();
    state.roots = Array.isArray(data) ? data : (data.roots || []);
    renderRoots();
  }
  function normalizeDrive(drive = {}) {
    const path = drive.path || drive.root || drive.name || '';
    return { path, label: drive.label || drive.volume_label || path, type: drive.type || drive.drive_type || 'unknown', accessible: Boolean(drive.accessible ?? drive.ready ?? true), selected: Boolean(drive.selected ?? ((drive.type || drive.drive_type) === 'fixed' && (drive.accessible ?? drive.ready ?? true))) };
  }
  function renderDrives() {
    const list = $('#driveList'), selectable = state.drives.filter(d => d.type === 'fixed' && d.accessible);
    if (!state.drives.length) { list.innerHTML = `<p class="quiet">${adapter.mode === 'desktop' ? '未发现可用本地磁盘' : '整机扫描仅在 Windows 桌面版提供'}</p>`; $('#computerScanBtn').disabled = true; return; }
    list.innerHTML = state.drives.map((drive, index) => `<label class="drive-option ${drive.accessible ? '' : 'unavailable'}"><input type="checkbox" data-drive-index="${index}" ${drive.selected ? 'checked' : ''} ${drive.type === 'fixed' && drive.accessible ? '' : 'disabled'}><span><strong>${escapeHtml(drive.path)}</strong><small>${escapeHtml(drive.label || '本地磁盘')}</small></span><span>${escapeHtml(drive.type === 'fixed' ? '固定磁盘' : drive.type)}</span></label>`).join('');
    $('#computerScanBtn').disabled = !selectable.some(d => d.selected);
  }
  async function loadDrives() {
    if (typeof adapter.listLocalDrives !== 'function') { renderDrives(); return; }
    try { const data = await adapter.listLocalDrives(); state.drives = (Array.isArray(data) ? data : (data.drives || [])).map(normalizeDrive); renderDrives(); }
    catch (error) { $('#driveList').innerHTML = `<p class="quiet">无法读取磁盘列表：${escapeHtml(error.message || error)}</p>`; }
  }
  async function loadMap() {
    const data = await adapter.map();
    const raw = Array.isArray(data) ? data : (data.nodes || data.items || []);
    state.expanded.clear();
    applyNodes(raw.map(normalizeNode));
    state.recommendations = data.recommendations || [];
    renderEmpty(!state.nodes.length);
    updateStats(data.stats || {});
    buildSidebar();
    fitView();
    draw();
  }
  function renderEmpty(show) { $('#emptyState').hidden = !show; if (show) stopAnim(); else ensureAnim(); }
  function updateStats(stats = {}) {
    const count = stats.files ?? stats.count ?? state.allNodes.length;
    const bytes = stats.bytes ?? stats.total_bytes ?? state.allNodes.reduce((s,n) => s + n.size, 0);
    const recs = stats.recommendations ?? state.recommendations.length;
    $('#mapStats').innerHTML = `<span class="stat-item"><strong>${Number(count).toLocaleString('zh-CN')}</strong> 个文件</span><span class="stat-item"><strong>${formatBytes(bytes)}</strong> 已观察</span><span class="stat-item"><strong>${recs}</strong> 个建议</span>`;
  }
  function loadDemo(data) {
    const raw = data && (data.nodes || data.items);
    state.expanded.clear();
    applyNodes((raw?.length ? raw : demoNodes).map(normalizeNode));
    state.demo = true; state.recommendations = [{node_id:'n3'},{node_id:'n6'},{node_id:'n11'}];
    $('#lastScan').textContent = '演示生态 · 本地样本';
    $('#connectionState').textContent = '演示模式';
    renderEmpty(false); updateStats(); buildSidebar(); fitView(); draw();
    toast('演示生态已苏醒，可拖拽并点击探索');
  }
  async function startDemo() {
    try { loadDemo(await adapter.demo()); } catch (error) { if (adapter.mode === 'desktop') toast(desktopError('载入演示生态', error)); loadDemo(); }
  }
  function beginStatusPolling() {
    clearTimeout(state.statusTimer);
    state.statusTimer = window.setTimeout(pollStatus, 650);
  }
  async function pollStatus() {
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
  async function startScan() {
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
  async function cancelScan() {
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

  function resize() {
    const rect = stage.getBoundingClientRect(), dpr = Math.min(devicePixelRatio || 1, 2);
    canvas.width = Math.max(1, rect.width * dpr); canvas.height = Math.max(1, rect.height * dpr);
    canvas.style.width = `${rect.width}px`; canvas.style.height = `${rect.height}px`; ctx.setTransform(dpr,0,0,dpr,0,0); draw();
  }
  function screen(node) { const r = stage.getBoundingClientRect(); return { x:r.width/2 + state.view.x + node.x*state.view.scale, y:r.height/2 + state.view.y + node.y*state.view.scale, radius:node.r*state.view.scale }; }
  function hash01(n) { const x = Math.sin(n * 127.1 + 311.7) * 43758.5453; return x - Math.floor(x); }
  function rgbOf(hex) { const n = parseInt(hex.slice(1), 16); return [n >> 16, (n >> 8) & 255, n & 255]; }
  function rgba(rgb, a) { return `rgba(${rgb[0]},${rgb[1]},${rgb[2]},${a})`; }
  function lift(rgb, n) { return [Math.min(255, rgb[0] + n), Math.min(255, rgb[1] + n), Math.min(255, rgb[2] + n)]; }
  function dim(rgb, k) { return [rgb[0] * k | 0, rgb[1] * k | 0, rgb[2] * k | 0]; }
  function sporePose(node, index) {
    const p = screen(node);
    const hovered = state.hover === String(node.id);
    const phase = reduceMotion.matches ? 0 : Math.sin(((state.clock || 0) / 1000) * 1.05 + index * 0.73);
    const r = p.radius * (hovered ? 1.07 : 1) * (1 + phase * 0.016);
    const tilt = hash01(index + 3) * 0.22 - 0.11;
    const squash = 0.93 + hash01(index + 9) * 0.09;
    return { x: p.x, y: p.y, r, rx: r, ry: r * squash, tilt, hovered, phase };
  }
  function sporePath(s) {
    ctx.beginPath();
    ctx.ellipse(s.x, s.y, s.rx, s.ry, s.tilt, 0, Math.PI * 2);
  }
  function ensureAnim() {
    if (state.anim || reduceMotion.matches || !state.nodes.length) return;
    const tick = (now) => {
      state.clock = now;
      state.anim = requestAnimationFrame(tick);
      if (state.dragging || document.hidden) return;
      const minGap = state.nodes.length > 100 ? 48 : 0;
      if (minGap && now - (state.lastDraw || 0) < minGap) return;
      state.lastDraw = now;
      draw();
    };
    state.anim = requestAnimationFrame(tick);
  }
  function stopAnim() {
    if (state.anim) cancelAnimationFrame(state.anim);
    state.anim = 0;
  }
  function draw() {
    const rect = stage.getBoundingClientRect();
    ctx.clearRect(0, 0, rect.width, rect.height);
    const ranked = [...state.nodes].sort((a, b) => a.r - b.r);
    const labeled = new Set([...state.nodes].sort((a, b) => b.r - a.r).slice(0, 14).map((n) => String(n.id)));
    // Aggregates carry a count that explains a chunk of the map, so they are
    // always named even when small.
    for (const node of state.nodes) if (node.aggregate) labeled.add(String(node.id));
    if (state.hover) labeled.add(state.hover);
    ranked.forEach((node, index) => drawSpore(node, index));
    // Captions are drawn largest-first and skipped when they would collide, so
    // a dense cluster shows a few readable names instead of a pile of mush.
    const claimed = [];
    [...state.nodes].sort((a, b) => b.r - a.r).forEach((node) => {
      const id = String(node.id);
      if (!labeled.has(id)) return;
      const index = ranked.indexOf(node);
      if (id !== state.hover && isOccluded(node)) return;
      const s = sporePose(node, index);
      if (s.r < 16 && !s.hovered && !node.aggregate) return;
      const box = { x: s.x, y: s.y + s.ry, w: 96, h: 22 };
      if (id !== state.hover && claimed.some(c => Math.abs(c.x - box.x) < (c.w + box.w) / 2 && Math.abs(c.y - box.y) < (c.h + box.h) / 2)) return;
      claimed.push(box);
      drawSporeCaption(node, index);
    });
  }
  function isOccluded(node) {
    const p = screen(node);
    return state.nodes.some((other) => {
      if (other === node || other.r <= node.r) return false;
      const o = screen(other);
      return Math.hypot(p.x - o.x, p.y - o.y) < o.radius * 0.7;
    });
  }
  function drawSpore(node, index) {
    const s = sporePose(node, index);
    if (s.r < 1 || s.x + s.r < 0 || s.x - s.r > stage.clientWidth || s.y + s.r < 0 || s.y - s.r > stage.clientHeight) return;
    const meta = zones[node.zone] || zones.active;
    const rgb = rgbOf(meta.color);
    const health = Math.max(0, Math.min(1, Number(node.health) / 100));
    ctx.save();

    // A soft halo only around meaningful bodies. Tiny specks stay flat so the
    // map reads as a field of light rather than a tray of glass beads.
    if (s.r > 9) {
      const bloomR = s.r * 2.1;
      const bloom = ctx.createRadialGradient(s.x, s.y, s.r * 0.8, s.x, s.y, bloomR);
      bloom.addColorStop(0, rgba(rgb, (s.hovered ? 0.2 : 0.11) + health * 0.06));
      bloom.addColorStop(1, rgba(rgb, 0));
      ctx.beginPath();
      ctx.arc(s.x, s.y, bloomR, 0, Math.PI * 2);
      ctx.fillStyle = bloom;
      ctx.fill();
    }

    // Flat fill with a gentle top-down lift: enough shape to feel alive,
    // not enough to look like a rendered sphere.
    const body = ctx.createLinearGradient(s.x, s.y - s.ry, s.x, s.y + s.ry);
    const base = node.zone === 'zombies' ? dim(rgb, 0.82) : rgb;
    body.addColorStop(0, rgba(lift(base, 26), s.hovered ? 0.95 : 0.86));
    body.addColorStop(1, rgba(dim(base, 0.6), s.hovered ? 0.9 : 0.8));
    sporePath(s);
    ctx.fillStyle = body;
    ctx.fill();

    // Health reads as a quiet inner disc, not a glowing nucleus.
    if (s.r > 14) {
      ctx.beginPath();
      ctx.ellipse(s.x, s.y, s.rx * (0.3 + health * 0.24), s.ry * (0.3 + health * 0.24), s.tilt, 0, Math.PI * 2);
      ctx.fillStyle = rgba(lift(base, 70), 0.16 + health * 0.16);
      ctx.fill();
    }
    ctx.restore();

    sporePath(s);
    ctx.strokeStyle = s.hovered ? 'rgba(244,248,236,0.9)' : rgba(lift(base, 40), node.zone === 'zombies' ? 0.3 : 0.5);
    ctx.lineWidth = s.hovered ? 2 : 1;
    ctx.stroke();

    // Zone signatures, kept to a single stroke each.
    if (node.zone === 'clones' && s.r > 16) {
      ctx.beginPath();
      ctx.ellipse(s.x, s.y, s.rx * 0.72, s.ry * 0.72, s.tilt, 0, Math.PI * 2);
      ctx.strokeStyle = rgba(lift(rgb, 40), 0.3);
      ctx.setLineDash([3, 4]);
      ctx.lineWidth = 1;
      ctx.stroke();
      ctx.setLineDash([]);
    }
    if (node.zone === 'decay' && s.r > 16) {
      ctx.beginPath();
      ctx.ellipse(s.x, s.y, s.rx * 0.84, s.ry * 0.84, s.tilt, 0.5, Math.PI * 1.4);
      ctx.strokeStyle = rgba(rgb, 0.4);
      ctx.lineWidth = 1.2;
      ctx.stroke();
    }
    if (node.zone === 'endangered' && s.r > 18) {
      ctx.beginPath();
      ctx.ellipse(s.x, s.y, s.rx * 1.18, s.ry * 1.18, s.tilt, 0, Math.PI * 2);
      ctx.strokeStyle = rgba(rgb, 0.26);
      ctx.setLineDash([2, 6]);
      ctx.lineWidth = 1;
      ctx.stroke();
      ctx.setLineDash([]);
    }
    // An aggregate stands for many files, so it wears a ring of satellite dots
    // instead of pretending to be one body.
    if (node.aggregate && s.r > 10) {
      const dots = 7;
      for (let i = 0; i < dots; i++) {
        const a = (i / dots) * Math.PI * 2 + s.tilt;
        ctx.beginPath();
        ctx.arc(s.x + Math.cos(a) * s.rx * 1.24, s.y + Math.sin(a) * s.ry * 1.24, Math.max(1.1, s.r * 0.07), 0, Math.PI * 2);
        ctx.fillStyle = rgba(lift(rgb, 30), 0.5);
        ctx.fill();
      }
    }
  }
  function drawSporeCaption(node, index) {
    const s = sporePose(node, index);
    if (s.r < 16 && !s.hovered) return;
    const showSize = s.hovered || s.r > 30;
    const fontSize = Math.max(11, Math.min(15, s.r * 0.3));
    const maxChars = s.r > 46 || s.hovered ? 14 : 8;
    const label = node.name.length > maxChars ? `${node.name.slice(0, maxChars - 1)}…` : node.name;
    ctx.save();
    if (s.r >= 34) {
      sporePath(s);
      ctx.clip();
      const bandY = s.y + s.ry * 0.18;
      const band = ctx.createLinearGradient(s.x, bandY, s.x, s.y + s.ry);
      band.addColorStop(0, 'rgba(6,12,9,0)');
      band.addColorStop(0.22, 'rgba(6,12,9,0.42)');
      band.addColorStop(1, 'rgba(6,12,9,0.7)');
      ctx.fillStyle = band;
      ctx.fillRect(s.x - s.rx, bandY, s.rx * 2, s.ry);
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillStyle = 'rgba(247,250,244,0.96)';
      ctx.font = `600 ${fontSize}px "Segoe UI Variable Text", "Segoe UI", "Microsoft YaHei UI", sans-serif`;
      ctx.fillText(label, s.x, s.y + s.ry * (showSize ? 0.42 : 0.52));
      if (showSize) {
        ctx.fillStyle = 'rgba(210,228,150,0.92)';
        ctx.font = `500 ${Math.max(11, fontSize * 0.76)}px "Segoe UI Variable Text", "Segoe UI", "Microsoft YaHei UI", sans-serif`;
        ctx.fillText(formatBytes(node.size), s.x, s.y + s.ry * 0.64);
      }
    } else {
      // No chip, just text with a soft shadow: a boxed label on every small
      // node turns the field into a wall of buttons.
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      ctx.font = `600 ${fontSize}px "Segoe UI Variable Text", "Segoe UI", "Microsoft YaHei UI", sans-serif`;
      ctx.shadowColor = 'rgba(4,9,7,0.95)';
      ctx.shadowBlur = 5;
      ctx.fillStyle = 'rgba(238,245,232,0.94)';
      ctx.fillText(label, s.x, s.y + s.ry + 5);
      if (showSize) {
        ctx.font = `500 ${Math.max(10, fontSize * 0.8)}px "Segoe UI Variable Text", "Segoe UI", "Microsoft YaHei UI", sans-serif`;
        ctx.fillStyle = 'rgba(198,222,134,0.86)';
        ctx.fillText(formatBytes(node.size), s.x, s.y + s.ry + 7 + fontSize);
      }
      ctx.shadowBlur = 0;
    }
    ctx.restore();
  }
  function hitTest(clientX, clientY) { const rect=canvas.getBoundingClientRect(), x=clientX-rect.left,y=clientY-rect.top; return [...state.nodes].reverse().find(n=>{const p=screen(n);return Math.hypot(x-p.x,y-p.y)<=p.radius*1.08;}); }
  function fitView() {
    if (!state.nodes.length) return; const maxX=Math.max(...state.nodes.map(n=>Math.abs(n.x)+n.r)), maxY=Math.max(...state.nodes.map(n=>Math.abs(n.y)+n.r));
    state.view.scale=Math.max(.42, Math.min(2.4, Math.min((stage.clientWidth-88)/(maxX*2),(stage.clientHeight-72)/(maxY*2)))); state.view.x=0;state.view.y=0;
  }
  function zoom(factor,cx=stage.clientWidth/2,cy=stage.clientHeight/2){const old=state.view.scale,next=Math.max(.35,Math.min(2.6,old*factor));state.view.x=(state.view.x+stage.clientWidth/2-cx)*(next/old)-stage.clientWidth/2+cx;state.view.y=(state.view.y+stage.clientHeight/2-cy)*(next/old)-stage.clientHeight/2+cy;state.view.scale=next;draw();}

  function buildSidebar() {
    const sums={};state.allNodes.forEach(n=>sums[n.zone]=(sums[n.zone]||0)+n.size);
    $('#zoneList').innerHTML=Object.entries(sums).sort((a,b)=>b[1]-a[1]).map(([key,size])=>`<button class="zone-row" data-zone="${key}"><i style="background:${zones[key].color};color:${zones[key].color}"></i><span class="zone-name">${zones[key].name}</span><small class="zone-size">${formatBytes(size)}</small></button>`).join('') || '<p class="quiet">暂无区域数据</p>';
    const avg=state.allNodes.reduce((s,n)=>s+n.health,0)/(state.allNodes.length||1);$('#healthScore').textContent=Math.round(avg);
    const notes=[['giants','△','巨物正在升温','大型文件占据了显著空间'],['clones','∞','发现分身群落','相似副本正在形成聚落'],['endangered','◇','一座濒危岛','稀有文件值得额外关注']].filter(([z])=>state.allNodes.some(n=>n.zone===z));
    $('#briefCards').innerHTML=(notes.length?notes:[['active','○','生态状态平稳','暂未发现显著异常']]).slice(0,3).map(([z,s,t,p])=>`<article class="brief-card clickable" data-zone="${z}"><span class="card-symbol">${s}</span><div class="brief-copy"><strong>${t}</strong><p>${p}</p></div></article>`).join('');
  }
  function focusZone(zone) { const group=state.nodes.filter(n=>n.zone===zone);if(!group.length)return;state.view.x=-(group.reduce((s,n)=>s+n.x,0)/group.length)*state.view.scale;state.view.y=-(group.reduce((s,n)=>s+n.y,0)/group.length)*state.view.scale;draw(); }
  async function openDetail(node) {
    let detail=node; if(!state.demo){try{detail={...node,...await adapter.node(node.id)};}catch(error){if(adapter.mode==='desktop')toast(desktopError('读取节点详情',error));}}
    const meta=zones[detail.zone]||zones.active;
    $('#drawerContent').innerHTML=`<span class="drawer-zone"><i style="background:${meta.color}"></i>${meta.name}</span><h2>${escapeHtml(detail.name)}</h2><p class="detail-path">${escapeHtml(detail.path||'路径未提供')}</p><div class="detail-metrics"><div class="detail-metric"><span class="metric-label">占用空间</span><strong class="metric-value">${formatBytes(detail.size)}</strong></div><div class="detail-metric"><span class="metric-label">生态健康度</span><strong class="metric-value">${detail.health ?? '—'} / 100</strong></div><div class="detail-metric"><span class="metric-label">类型</span><strong class="metric-value">${escapeHtml(detail.kind||'文件')}</strong></div><div class="detail-metric"><span class="metric-label">最近变化</span><strong class="metric-value">${escapeHtml(detail.modified||'未知')}</strong></div></div><section class="drawer-section"><h3>Agent 观察</h3><p>${escapeHtml(detail.insight||meta.description)}。此处只呈现观察结果，不会自动执行任何文件操作。</p></section><div class="drawer-actions"><button class="primary" data-action="reveal">在资源管理器中定位</button><button data-action="ignore">忽略建议</button><button data-action="later">稍后处理</button></div>`;
    $('#detailDrawer').dataset.id=detail.id;$('#detailDrawer').classList.add('open');$('#detailDrawer').setAttribute('aria-hidden','false');$('#backdrop').hidden=false;
  }
  function closeDrawer(){ $('#detailDrawer').classList.remove('open');$('#detailDrawer').setAttribute('aria-hidden','true');$('#backdrop').hidden=true; }
  async function drawerAction(action) {
    const id=$('#detailDrawer').dataset.id,node=state.allNodes.find(n=>String(n.id)===String(id));
    if(action==='ignore'){try{if(!state.demo)await adapter.ignoreRecommendation(id);toast('已忽略这条建议');closeDrawer();}catch(error){toast(adapter.mode==='desktop'?desktopError('忽略建议',error):'暂时无法保存忽略状态');}}
    if(action==='later') toast('已留在稍后清单，不会修改文件');
    if(action==='reveal'){try{if(!state.demo)await adapter.revealNode(id);else throw new Error();toast('已请求资源管理器定位');}catch(error){if(node?.path && navigator.clipboard)navigator.clipboard.writeText(node.path).catch(()=>{});toast(adapter.mode==='desktop'?desktopError('在资源管理器中定位',error):'路径已复制，可在资源管理器中打开');}}
  }
  function renderRoots(){ $('#rootsList').innerHTML=state.roots.length?state.roots.map((r,i)=>`<div class="root-row"><span class="root-path">⌁ ${escapeHtml(r.path||r)}</span><button type="button" data-remove-root="${i}" aria-label="移除授权">移除</button></div>`).join(''):'<p class="quiet">尚未授权任何观察根目录</p>'; }
  async function addRoot(){const input=$('#rootPath'),path=input.value.trim();if(!path)return;try{if(!adapter.addRoot)throw new Error('请使用原生目录选择按钮');const data=await adapter.addRoot(path);state.roots=data.roots||[...state.roots,{path}];input.value='';renderRoots();toast('根目录已授权，仅用于只读观察');}catch(error){toast(adapter.mode==='desktop'?desktopError('添加根目录',error):'无法保存授权，请确认本地 Agent 已启动');}}
  async function chooseRoot(){
    const button=$('#chooseRootBtn');
    try{
      if(typeof adapter.chooseDirectory!=='function'){$('#rootPath').focus();toast('HTTP 开发模式请手动输入目录路径');return;}
      button.disabled=true;const chosen=await adapter.chooseDirectory();const path=typeof chosen==='string'?chosen:(chosen?.path||chosen?.directory||'');if(chosen?.cancelled||chosen?.canceled||!path)return;
      const data=await adapter.addRoot(path);state.roots=data.roots||[...state.roots,{path}];renderRoots();toast('文件夹已授权，仅用于只读观察');
    }catch(error){toast(desktopError('选择目录',error));}finally{button.disabled=false;}
  }
  function selectedDrives(){return state.drives.filter(d=>d.selected&&d.type==='fixed'&&d.accessible);}
  function openComputerScan(){const drives=selectedDrives();if(!drives.length){toast('请至少选择一个可用固定磁盘');return;}$('#computerScanSummary').innerHTML=drives.map(d=>`<div class="scope-drive"><strong>${escapeHtml(d.path)}</strong><span>${escapeHtml(d.label||'本地固定磁盘')}</span></div>`).join('');$('#computerScanDialog').showModal();}
  async function confirmComputerScan(){const drives=selectedDrives(),button=$('#confirmComputerScanBtn');if(!drives.length)return;button.disabled=true;button.textContent='正在授权…';try{const data=await adapter.addRootBatch(drives.map(d=>d.path),true);if(!data.authorization_succeeded)throw new Error((data.results||[]).filter(r=>r.error).map(r=>`${r.requested_path}: ${r.error}`).join('；')||'磁盘授权未完成');state.roots=data.roots||state.roots;renderRoots();$('#computerScanDialog').close();$('#settingsDialog').close();if(data.scan_error)throw new Error(`磁盘已授权，但扫描未开始：${data.scan_error}`);state.demo=false;state.scanProgress.startedAt=Date.now();state.scanProgress.failures=0;state.scanProgress.taskId=String(data.scan?.scan_id||'');setScanUI(true);renderScanProgress({scanning:true,progress:{phase:'starting'}});toast(`已授权 ${drives.length} 个本地磁盘，开始只读扫描`);beginStatusPolling();}catch(error){toast(`整机扫描无法开始：${error.message||error}`);}finally{button.disabled=false;button.textContent='授权并开始扫描';}}
  async function removeRoot(index){const root=state.roots[index],path=root.path||root;try{const data=await adapter.removeRoot(path);state.roots=data?.roots||(state.roots.filter((_,i)=>i!==index));renderRoots();toast('已移除观察授权，原文件不受影响');}catch(error){toast(adapter.mode==='desktop'?desktopError('移除根目录',error):'暂时无法移除授权');}}
  function profileFormValue() {
    const base_url = $('#agentBaseUrl').value.trim();
    let model = $('#agentModel').value.trim();
    const aliases = { 'deepseek-flash': 'deepseek-v4-flash', 'deepseek-v4': 'deepseek-v4-flash', 'deepseek-pro': 'deepseek-v4-pro' };
    if (/api\.deepseek\.com/i.test(base_url) && aliases[model.toLowerCase()]) {
      model = aliases[model.toLowerCase()];
      $('#agentModel').value = model;
    }
    return { provider: document.querySelector('input[name="providerType"]:checked')?.value || 'cloud', base_url, model };
  }
  function explainAgentError(error) {
    const raw = String(error?.message || error || '');
    if (/model profile is not configured/i.test(raw)) return '还没有保存模型配置。请先点「保存设置」，再测试连接。';
    if (/API key is not configured/i.test(raw)) return '云端模型需要 API Key。请填写后先保存，再测试。';
    if (/model network access is disabled/i.test(raw)) return '模型联网仍未开启。请打开「允许模型联网」并保存。';
    if (/invalid model base URL/i.test(raw)) return 'Base URL 无效。DeepSeek 请填 https://api.deepseek.com';
    if (/HTTP 401|Authentication Fails|api key.*invalid/i.test(raw)) return 'DeepSeek 拒绝了密钥（HTTP 401）。请到 platform.deepseek.com 复制新的 API Key，在设置里重新保存后再巡视。';
    if (/HTTP 402|Insufficient Balance/i.test(raw)) return 'DeepSeek 账户余额不足，请先充值。';
    if (/HTTP 404|Model Not Exist|invalid model/i.test(raw)) return '模型名无效。请改成 deepseek-v4-flash 或 deepseek-v4-pro。';
    if (/HTTP 429|rate limited/i.test(raw)) return '请求太频繁，稍后再试。';
    if (/unsafe provider address|proxyconnect/i.test(raw)) return '本机代理（Clash 等）被安全校验拦住了。请更新到最新版本后再测；云端请求可以走本地代理。';
    return raw || '请检查地址、模型和密钥';
  }
  async function persistAgentSettings() {
    const profile = profileFormValue();
    if (!profile.base_url || !profile.model) throw new Error('请填写 Base URL 与模型名称。');
    await adapter.saveModelProfile(profile);
    const key = $('#agentApiKey').value;
    if (key) await adapter.setAPIKey(key);
    await adapter.setNetworkEnabled($('#networkEnabled').checked);
    state.model = { ...state.model, ...normalizeProfile(profile), network_enabled: $('#networkEnabled').checked, has_api_key: state.model.has_api_key || Boolean(key) };
    if (key) { $('#agentApiKey').value = ''; $('#agentApiKey').placeholder = '已安全保存 · 留空保持不变'; }
    updateTrustStrip();
    return profile;
  }
  async function saveAgentSettings() {
    const status = $('#agentSettingsStatus');
    status.textContent = '正在保存到本地安全存储…';
    try {
      await persistAgentSettings();
      status.textContent = '设置已保存在本机。';
      toast('Agent 设置已保存');
    } catch (error) { status.textContent = `保存失败：${explainAgentError(error)}`; }
  }
  async function testAgentConnection() {
    const button = $('#testAgentBtn'), status = $('#agentSettingsStatus');
    button.disabled = true;
    status.textContent = '正在保存并测试连接…';
    try {
      await persistAgentSettings();
      const result = await adapter.testModelConnection();
      status.textContent = result?.message || `连接成功${result?.latency_ms ? ` · ${result.latency_ms} ms` : ''}`;
      toast('模型连接成功');
    } catch (error) { status.textContent = `连接失败：${explainAgentError(error)}`; }
    finally { button.disabled = false; }
  }
  function payloadText(payload) { return typeof payload === 'string' ? payload : JSON.stringify(payload || {}, null, 2); }
  function previewFields(data = {}) {
    const payload = data.payload ?? data.body ?? data.agent_payload ?? {};
    const text = payloadText(payload);
    return { raw: data, payload, text, target: data.target_origin || data.target || data.origin || data.base_url || state.model.base_url || (state.model.provider_type === 'local' ? '本地模型' : '未配置'), hash: data.hash || data.payload_hash || '由服务端确认时校验', bytes: Number(data.payload_bytes || data.bytes || data.size || new TextEncoder().encode(text).length), confirmation: data.confirmation_token || data.confirmation || data.token || '' };
  }
  async function openAgentPreview() {
    if (!state.nodes.length) { toast('请先完成一次只读扫描，再让 Agent 巡视'); return; }
    if (!state.model.model) { $('#settingsDialog').showModal(); switchSettingsTab('agent'); toast('请先配置 Agent 模型'); return; }
    $('#agentPreviewDialog').showModal(); $('#agentPayloadPreview').textContent = '正在由本地服务生成匿名 payload…'; $('#confirmAgentBtn').disabled = true;
    try {
      state.agent.preview = previewFields(await adapter.previewAgentRun());
      if (!state.agent.preview.confirmation || !state.agent.preview.hash || state.agent.preview.hash === '由服务端确认时校验') throw new Error('预览缺少确认令牌或 Payload Hash');
      state.agent.lastPayload = state.agent.preview.payload;
      $('#previewTarget').textContent = state.agent.preview.target; $('#previewHash').textContent = state.agent.preview.hash; $('#previewHash').title = state.agent.preview.hash; $('#previewSize').textContent = formatBytes(state.agent.preview.bytes); $('#agentPayloadPreview').textContent = state.agent.preview.text; $('#confirmAgentBtn').disabled = false;
    } catch (error) { $('#agentPayloadPreview').textContent = `无法生成预览：${error.message || '本地服务尚未支持 Agent API'}`; }
  }
  function openAgentDrawer() { $('#agentDrawer').classList.add('open'); $('#agentDrawer').setAttribute('aria-hidden','false'); $('#backdrop').hidden=false; }
  function renderAgentSteps(steps = [], runStatus = '') {
    const failed = ['failed', 'error'].includes(String(runStatus).toLowerCase());
    $('#agentSteps').innerHTML = steps.map((step,i) => {
      const kind = String(step.kind || '');
      let status = step.status || step.state || (step.completed ? 'done' : (kind === 'error' ? 'failed' : (kind === 'response' ? 'done' : 'pending')));
      if (failed && kind === 'request' && i === steps.length - 1) status = 'failed';
      const label = status === 'failed' ? '失败' : (status === 'done' || status === 'completed' ? '完成' : (status === 'running' ? '运行中' : '进行中'));
      return `<div class="agent-step ${escapeHtml(status)}"><i>${status==='done'||status==='completed'?'✓':(status==='failed'?'!':i+1)}</i><div><strong>${escapeHtml(step.title||step.name||step.tool||`分析步骤 ${i+1}`)}</strong><small>${escapeHtml(step.detail||step.summary||step.message||'等待 Agent')}</small></div><span>${escapeHtml(step.duration_ms ? `${step.duration_ms} ms` : label)}</span></div>`;
    }).join('') || '<div class="agent-step running"><i>1</i><div><strong>建立只读调查计划</strong><small>Agent 正在检查已确认的匿名证据</small></div><span>运行中</span></div>';
  }
  async function confirmAgentRun() {
    const preview = state.agent.preview; if (!preview) return;
    $('#confirmAgentBtn').disabled = true;
    try {
      const result = await adapter.startAgentRun(preview.hash, preview.confirmation);
      state.agent.runId = result.run_id || result.id || ''; state.agent.running = true; state.agent.startedAt = Date.now();
      $('#agentPreviewDialog').close(); openAgentDrawer(); $('#agentRunState').textContent = '巡视中'; $('#cancelAgentBtn').hidden = false; $('#agentReport').hidden = true; renderAgentSteps(result.steps || []); pollAgentRun();
    } catch (error) { $('#confirmAgentBtn').disabled = false; toast(`无法开始 Agent 巡视：${error.message || '确认已失效'}`); }
  }
  function applyAgentRun(data = {}) {
    const status = String(data.status || data.state || 'running').toLowerCase(), steps = data.steps || [];
    renderAgentSteps(steps, status); const done = steps.filter(s => ['done','completed','success'].includes(String(s.status||s.state||s.kind).toLowerCase()) || String(s.kind).toLowerCase()==='response').length; $('#agentProgressBar').style.width = `${status==='completed'||status==='done'||status==='failed'?100:Math.max(8,steps.length ? done/Math.max(steps.length,1)*100 : 12)}%`;
    $('#agentRunState').textContent = ({completed:'已完成',done:'已完成',failed:'失败',cancelled:'已取消',canceled:'已取消',running:'巡视中',cancelling:'取消中'})[status] || status;
    $('#agentRunState').classList.toggle('failed', status==='failed');
    const errBox = $('#agentRunError');
    const errText = data.error || data.message || data.last_error || '';
    if (status === 'failed' && errText) {
      errBox.hidden = false;
      errBox.textContent = explainAgentError({ message: errText });
    } else {
      errBox.hidden = true;
      errBox.textContent = '';
    }
    const usage = data.usage || {}; $('#agentTokens').textContent = (usage.total_tokens ?? data.tokens ?? '—').toLocaleString?.('zh-CN') || '—'; $('#agentDuration').textContent = data.duration_ms ? `${(data.duration_ms/1000).toFixed(1)} s` : `${((Date.now()-state.agent.startedAt)/1000).toFixed(1)} s`; $('#agentConfidence').textContent = data.confidence != null ? `${Math.round(Number(data.confidence)* (Number(data.confidence)<=1?100:1))}%` : '—';
    return ['completed','done','failed','cancelled','canceled'].includes(status);
  }
  async function pollAgentRun() {
    clearTimeout(state.agent.timer);
    try {
      const data = await adapter.agentRunStatus(state.agent.runId);
      try { const audit = await adapter.listAgentAudits(state.agent.runId); data.steps = Array.isArray(audit) ? audit : (audit.steps || []); } catch (_) {}
      if (!applyAgentRun(data)) { state.agent.timer = setTimeout(pollAgentRun, 700); return; }
      state.agent.running = false; $('#cancelAgentBtn').hidden = true;
      if (['completed','done','failed'].includes(String(data.status||data.state).toLowerCase())) await loadAgentResult(data);
    } catch (error) { state.agent.running=false; $('#agentRunState').textContent='状态异常'; $('#agentRunState').classList.add('failed'); $('#cancelAgentBtn').hidden=true; toast(`无法读取 Agent 状态：${error.message}`); }
  }
  async function loadAgentResult(statusData = {}) {
    let data = statusData; try { data = { ...statusData, ...await adapter.agentRunResult(state.agent.runId) }; } catch (_) {}
    let report = data.report || data.result || {};
    if (typeof report === 'string') { try { report = JSON.parse(report); } catch (_) { report = { text: report }; } }
    const failed = ['failed','error'].includes(String(data.status||data.state||'').toLowerCase());
    const body = report.final?.content || report.summary || report.markdown || report.text || report.overview || (failed ? (data.error || '巡视失败，模型没有返回报告。') : 'Agent 已完成巡视，但未返回报告正文。');
    $('#agentReportBody').innerHTML = `<div class="report-prose">${renderMarkdown(body)}</div>`;
    const evidence = report.evidence || report.tools_called || data.evidence || []; $('#agentEvidence').innerHTML = evidence.length ? evidence.map(item=>`<div class="evidence-item">${escapeHtml(typeof item==='string'?item:(item.summary||item.description||JSON.stringify(item)))}</div>`).join('') : '<p class="quiet">报告未引用额外证据</p>'; $('#agentReport').hidden=false; applyAgentRun(data);
  }
  async function cancelAgentRun() { if(!state.agent.running)return; $('#agentRunState').textContent='取消中'; try{await adapter.cancelAgentRun(state.agent.runId);pollAgentRun();}catch(error){toast(`取消巡视失败：${error.message}`);} }

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
  async function openRecycle() {
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
  async function reviewRecycle() {
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
  async function confirmRecycle() {
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
  async function openPrivacy(){
    $('#privacyDialog').showModal();
    try { const data=await adapter.privacy(); if(data.capabilities) $('#privacyAudit').innerHTML=Object.entries(data.capabilities).map(([k,v])=>`<div><span>${escapeHtml(k)}</span><strong class="${v===false?'safe':''}">${escapeHtml(v===false?'禁用':String(v))}</strong></div>`).join(''); const payload=data.agent_payload||data.payload||state.agent.lastPayload; $('#privacyPayload').textContent=payload?payloadText(payload):'尚无可展示的 payload'; }
    catch(error){if(adapter.mode==='desktop')toast(desktopError('读取隐私审计',error));}
    try { const data=state.agent.runId ? await adapter.listAgentAudits(state.agent.runId) : {steps:[]}; state.agent.audits=Array.isArray(data)?data:(data.runs||data.audits||data.steps||[]); $('#auditRuns').innerHTML=state.agent.audits.length?state.agent.audits.map(run=>`<div class="audit-run"><strong>${escapeHtml(run.model||run.target||run.name||'Agent 巡视')}</strong><span>${escapeHtml(run.status||run.state||run.kind||'已记录')}</span><small>${escapeHtml(run.created_at||run.time||run.at||'')} · ${escapeHtml(run.payload_hash||run.detail||'匿名步骤')} · ${formatBytes(run.payload_bytes||run.bytes||0)}</small></div>`).join(''):'<p class="quiet">尚无 Agent 运行记录</p>'; } catch(_) {}
  }
  function switchSettingsTab(name){document.querySelectorAll('[data-settings-tab]').forEach(b=>b.classList.toggle('active',b.dataset.settingsTab===name));$('#rootsSettings').hidden=name!=='roots';$('#agentSettings').hidden=name!=='agent';}
  function switchAuditTab(name){document.querySelectorAll('[data-audit-tab]').forEach(b=>b.classList.toggle('active',b.dataset.auditTab===name));$('#boundaryAudit').hidden=name!=='boundary';$('#payloadAudit').hidden=name!=='payload';$('#runsAudit').hidden=name!=='runs';}
  function escapeHtml(value){const el=document.createElement('span');el.textContent=String(value);return el.innerHTML;}
  function renderMarkdown(src) {
    const lines = String(src || '').replace(/\r\n/g, '\n').split('\n');
    const out = [];
    let list = null;
    const flush = () => { if (!list) return; out.push(`<${list.tag}>${list.items.join('')}</${list.tag}>`); list = null; };
    const inline = (s) => escapeHtml(s)
      .replace(/\*\*\*(.+?)\*\*\*/g, '<strong>$1</strong>')
      .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
      .replace(/`([^`]+)`/g, '<code>$1</code>');
    for (const line of lines) {
      const heading = /^(#{1,3})\s+(.+)$/.exec(line);
      if (heading) { flush(); const level = heading[1].length + 2; out.push(`<h${level}>${inline(heading[2])}</h${level}>`); continue; }
      const bullet = /^[-*]\s+(.+)$/.exec(line);
      if (bullet) { if (!list || list.tag !== 'ul') { flush(); list = { tag: 'ul', items: [] }; } list.items.push(`<li>${inline(bullet[1])}</li>`); continue; }
      const numbered = /^\d+\.\s+(.+)$/.exec(line);
      if (numbered) { if (!list || list.tag !== 'ol') { flush(); list = { tag: 'ol', items: [] }; } list.items.push(`<li>${inline(numbered[1])}</li>`); continue; }
      if (!line.trim()) { flush(); continue; }
      flush();
      out.push(`<p>${inline(line)}</p>`);
    }
    flush();
    return out.join('') || '<p class="quiet">报告为空</p>';
  }
  function toast(message){const el=document.createElement('div');el.className='toast';el.textContent=message;$('#toastRegion').appendChild(el);setTimeout(()=>el.remove(),2800);}
  function search(query){const q=query.trim().toLowerCase(),box=$('#searchResults');if(!q){box.hidden=true;return;}const found=state.allNodes.filter(n=>`${n.name} ${n.path} ${zones[n.zone].name}`.toLowerCase().includes(q)).slice(0,7);box.innerHTML=found.length?found.map(n=>`<button class="search-result" data-node="${escapeHtml(n.id)}"><strong>${escapeHtml(n.name)}</strong><small class="result-path">${escapeHtml(n.path||zones[n.zone].name)} · ${formatBytes(n.size)}</small></button>`).join(''):'<div class="search-result"><small class="result-path">没有找到匹配的生态节点</small></div>';box.hidden=false;}

  canvas.addEventListener('pointerdown',e=>{state.dragging=true;state.moved=false;state.last={x:e.clientX,y:e.clientY};canvas.setPointerCapture(e.pointerId);canvas.classList.add('dragging');});
  canvas.addEventListener('pointermove',e=>{if(state.dragging){const dx=e.clientX-state.last.x,dy=e.clientY-state.last.y;if(Math.abs(dx)+Math.abs(dy)>2)state.moved=true;state.view.x+=dx;state.view.y+=dy;state.last={x:e.clientX,y:e.clientY};draw();return;}const n=hitTest(e.clientX,e.clientY);state.hover=n?String(n.id):null;canvas.style.cursor=n?'pointer':'grab';const tip=$('#mapTooltip');if(n){tip.innerHTML=`<strong>${escapeHtml(n.name)}</strong><span class="tip-meta"><i class="tip-dot" style="background:${zones[n.zone].color}"></i>${zones[n.zone].name} · ${formatBytes(n.size)}${n.aggregate?' · 点击展开':''}</span>`;tip.hidden=false;const r=stage.getBoundingClientRect();tip.style.left=`${Math.min(e.clientX-r.left+14,r.width-220)}px`;tip.style.top=`${Math.min(e.clientY-r.top+14,r.height-72)}px`;}else tip.hidden=true;draw();});
  canvas.addEventListener('pointerup',e=>{if(!state.moved){const n=hitTest(e.clientX,e.clientY);if(n){if(n.aggregate)expandAggregate(n.id);else openDetail(n);}}state.dragging=false;canvas.classList.remove('dragging');});
  canvas.addEventListener('pointerleave',()=>{$('#mapTooltip').hidden=true;state.hover=null;draw();});
  canvas.addEventListener('wheel',e=>{e.preventDefault();const r=stage.getBoundingClientRect();zoom(e.deltaY<0?1.12:.89,e.clientX-r.left,e.clientY-r.top);},{passive:false});
  $('#zoomIn').onclick=()=>zoom(1.18);$('#zoomOut').onclick=()=>zoom(.84);$('#resetView').onclick=()=>{fitView();draw();};
  $('#scanBtn').onclick=startScan;$('#demoBtn').onclick=startDemo;$('#settingsBtn').onclick=()=>{$('#settingsDialog').showModal();loadDrives();};$('#emptyRootBtn').onclick=()=>{$('#settingsDialog').showModal();loadDrives();};$('#privacyBtn').onclick=openPrivacy;$('#addRootBtn').onclick=addRoot;$('#chooseRootBtn').onclick=chooseRoot;$('#computerScanBtn').onclick=openComputerScan;$('#confirmComputerScanBtn').onclick=confirmComputerScan;
  $('#agentPatrolBtn').onclick=openAgentPreview;$('#confirmAgentBtn').onclick=confirmAgentRun;$('#cancelAgentBtn').onclick=cancelAgentRun;$('#closeAgentDrawer').onclick=()=>{ $('#agentDrawer').classList.remove('open');$('#agentDrawer').setAttribute('aria-hidden','true');$('#backdrop').hidden=true; };$('#saveAgentBtn').onclick=saveAgentSettings;$('#testAgentBtn').onclick=testAgentConnection;
  $('.settings-tabs').onclick=e=>{const b=e.target.closest('[data-settings-tab]');if(b)switchSettingsTab(b.dataset.settingsTab);};$('.audit-tabs').onclick=e=>{const b=e.target.closest('[data-audit-tab]');if(b)switchAuditTab(b.dataset.auditTab);};
  $('#networkEnabled').onchange=()=>{if(!$('#networkEnabled').checked&&state.agent.running)cancelAgentRun();};
  $('#rootsList').onclick=e=>{const button=e.target.closest('[data-remove-root]');if(button)removeRoot(Number(button.dataset.removeRoot));};$('#driveList').onchange=e=>{const input=e.target.closest('[data-drive-index]');if(!input)return;const drive=state.drives[Number(input.dataset.driveIndex)];if(drive)drive.selected=input.checked;$('#computerScanBtn').disabled=!selectedDrives().length;};
  $('#recycleBtn').onclick=openRecycle;$('#recycleReviewBtn').onclick=reviewRecycle;$('#confirmRecycleBtn').onclick=confirmRecycle;
  $('#recycleList').onchange=e=>{const input=e.target.closest('[data-recycle-index]');if(!input)return;const item=state.recycle.candidates[Number(input.dataset.recycleIndex)];if(!item)return;if(input.checked)state.recycle.selected.add(item.entry_id);else state.recycle.selected.delete(item.entry_id);updateRecycleSummary();};
  $('#zoneList').onclick=e=>{const b=e.target.closest('[data-zone]');if(b)focusZone(b.dataset.zone);};$('#briefCards').onclick=e=>{const b=e.target.closest('[data-zone]');if(b)focusZone(b.dataset.zone);};
  $('.drawer-close').onclick=closeDrawer;$('#backdrop').onclick=()=>{closeDrawer();$('#agentDrawer').classList.remove('open');$('#agentDrawer').setAttribute('aria-hidden','true');};$('#drawerContent').onclick=e=>{const b=e.target.closest('[data-action]');if(b)drawerAction(b.dataset.action);};
  $('#searchInput').addEventListener('input',e=>search(e.target.value));$('#searchResults').onclick=e=>{const b=e.target.closest('[data-node]');if(!b)return;const n=state.allNodes.find(n=>String(n.id)===b.dataset.node);if(n){if(!state.nodes.includes(n))expandAggregate(`agg:${n.zone}`);openDetail(n);}$('#searchResults').hidden=true;};
  document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();$('#searchInput').focus();$('#searchInput').select();}if(e.key==='Escape')closeDrawer();});
  new ResizeObserver(resize).observe(stage); bootstrap();
})();
