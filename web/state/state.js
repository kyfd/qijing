// 全局前端状态：所有模块读写同一份对象，避免在模块之间传递可变副本。
export const state = { token: '', nodes: [], allNodes: [], expanded: new Set(), truncation: null, roots: [], drives: [], recommendations: [], view: { x: 0, y: 0, scale: 1 }, dragging: false, moved: false, last: null, hover: null, demo: false, scanning: false, cancelling: false, statusTimer: 0, scanProgress: { startedAt: 0, failures: 0, last: null, taskId: '' }, model: { provider_type: 'cloud', base_url: '', model: '', network_enabled: false, has_api_key: false }, agent: { preview: null, runId: '', running: false, timer: 0, startedAt: 0, lastPayload: null, audits: [] }, recycle: { candidates: [], selected: new Set(), preview: null, busy: false } };

// 生态分区：地图着色、侧栏汇总与详情抽屉共用同一份定义。
export const zones = {
  active: { name: '活跃森林', color: '#6fd48c', description: '近期频繁生长与访问的文件' },
  seedlings: { name: '幼苗区', color: '#c6e56a', description: '新近出现、仍在成长的文件' },
  downloads: { name: '下载荒原', color: '#e0ad5a', description: '下载后鲜少再被访问的沉积' },
  zombies: { name: '僵尸墓地', color: '#8f9d96', description: '长久沉睡但仍占据空间的文件' },
  giants: { name: '巨物火山', color: '#e07a52', description: '占用显著的超大型文件' },
  clones: { name: '分身群落', color: '#7eb8d0', description: '内容相同或高度相似的副本' },
  decay: { name: '腐烂区', color: '#c48a62', description: '临时、缓存或可能已失效的文件' },
  endangered: { name: '濒危岛', color: '#c9a0d4', description: '稀有、孤立且值得备份的文件' }
};

export const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)');

export const demoNodes = [
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
