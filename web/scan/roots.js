// 观察根目录的授权管理：手工输入、原生目录选择、整机磁盘扫描与移除。
// 所有授权都只用于只读观察；移除授权不影响文件本身。
import { state } from '../state/state.js';
import { $, escapeHtml, toast } from '../components/dom.js';
import { adapter, desktopError } from '../api/adapter.js';
import { setScanUI, renderScanProgress } from './ui.js';
import { beginStatusPolling } from './controller.js';

export function renderRoots(){ $('#rootsList').innerHTML=state.roots.length?state.roots.map((r,i)=>`<div class="root-row"><span class="root-path">⌁ ${escapeHtml(r.path||r)}</span><button type="button" data-remove-root="${i}" aria-label="移除授权">移除</button></div>`).join(''):'<p class="quiet">尚未授权任何观察根目录</p>'; }

export async function loadRoots() {
  const data = await adapter.listRoots();
  state.roots = Array.isArray(data) ? data : (data.roots || []);
  renderRoots();
}

export async function addRoot(){const input=$('#rootPath'),path=input.value.trim();if(!path)return;try{if(!adapter.addRoot)throw new Error('请使用原生目录选择按钮');const data=await adapter.addRoot(path);state.roots=data.roots||[...state.roots,{path}];input.value='';renderRoots();toast('根目录已授权，仅用于只读观察');}catch(error){toast(adapter.mode==='desktop'?desktopError('添加根目录',error):'无法保存授权，请确认本地 Agent 已启动');}}

export async function chooseRoot(){
  const button=$('#chooseRootBtn');
  try{
    if(typeof adapter.chooseDirectory!=='function'){$('#rootPath').focus();toast('HTTP 开发模式请手动输入目录路径');return;}
    button.disabled=true;const chosen=await adapter.chooseDirectory();const path=typeof chosen==='string'?chosen:(chosen?.path||chosen?.directory||'');if(chosen?.cancelled||chosen?.canceled||!path)return;
    const data=await adapter.addRoot(path);state.roots=data.roots||[...state.roots,{path}];renderRoots();toast('文件夹已授权，仅用于只读观察');
  }catch(error){toast(desktopError('选择目录',error));}finally{button.disabled=false;}
}

function normalizeDrive(drive = {}) {
  const path = drive.path || drive.root || drive.name || '';
  return { path, label: drive.label || drive.volume_label || path, type: drive.type || drive.drive_type || 'unknown', accessible: Boolean(drive.accessible ?? drive.ready ?? true), selected: Boolean(drive.selected ?? ((drive.type || drive.drive_type) === 'fixed' && (drive.accessible ?? drive.ready ?? true))) };
}

export function renderDrives() {
  const list = $('#driveList'), selectable = state.drives.filter(d => d.type === 'fixed' && d.accessible);
  if (!state.drives.length) { list.innerHTML = `<p class="quiet">${adapter.mode === 'desktop' ? '未发现可用本地磁盘' : '整机扫描仅在 Windows 桌面版提供'}</p>`; $('#computerScanBtn').disabled = true; return; }
  list.innerHTML = state.drives.map((drive, index) => `<label class="drive-option ${drive.accessible ? '' : 'unavailable'}"><input type="checkbox" data-drive-index="${index}" ${drive.selected ? 'checked' : ''} ${drive.type === 'fixed' && drive.accessible ? '' : 'disabled'}><span><strong>${escapeHtml(drive.path)}</strong><small>${escapeHtml(drive.label || '本地磁盘')}</small></span><span>${escapeHtml(drive.type === 'fixed' ? '固定磁盘' : drive.type)}</span></label>`).join('');
  $('#computerScanBtn').disabled = !selectable.some(d => d.selected);
}

export async function loadDrives() {
  if (typeof adapter.listLocalDrives !== 'function') { renderDrives(); return; }
  try { const data = await adapter.listLocalDrives(); state.drives = (Array.isArray(data) ? data : (data.drives || [])).map(normalizeDrive); renderDrives(); }
  catch (error) { $('#driveList').innerHTML = `<p class="quiet">无法读取磁盘列表：${escapeHtml(error.message || error)}</p>`; }
}

export function selectedDrives(){return state.drives.filter(d=>d.selected&&d.type==='fixed'&&d.accessible);}

export function openComputerScan(){const drives=selectedDrives();if(!drives.length){toast('请至少选择一个可用固定磁盘');return;}$('#computerScanSummary').innerHTML=drives.map(d=>`<div class="scope-drive"><strong>${escapeHtml(d.path)}</strong><span>${escapeHtml(d.label||'本地固定磁盘')}</span></div>`).join('');$('#computerScanDialog').showModal();}

export async function confirmComputerScan(){const drives=selectedDrives(),button=$('#confirmComputerScanBtn');if(!drives.length)return;button.disabled=true;button.textContent='正在授权…';try{const data=await adapter.addRootBatch(drives.map(d=>d.path),true);if(!data.authorization_succeeded)throw new Error((data.results||[]).filter(r=>r.error).map(r=>`${r.requested_path}: ${r.error}`).join('；')||'磁盘授权未完成');state.roots=data.roots||state.roots;renderRoots();$('#computerScanDialog').close();$('#settingsDialog').close();if(data.scan_error)throw new Error(`磁盘已授权，但扫描未开始：${data.scan_error}`);state.demo=false;state.scanProgress.startedAt=Date.now();state.scanProgress.failures=0;state.scanProgress.taskId=String(data.scan?.scan_id||'');setScanUI(true);renderScanProgress({scanning:true,progress:{phase:'starting'}});toast(`已授权 ${drives.length} 个本地磁盘，开始只读扫描`);beginStatusPolling();}catch(error){toast(`整机扫描无法开始：${error.message||error}`);}finally{button.disabled=false;button.textContent='授权并开始扫描';}}

export async function removeRoot(index){const root=state.roots[index],path=root.path||root;try{const data=await adapter.removeRoot(path);state.roots=data?.roots||(state.roots.filter((_,i)=>i!==index));renderRoots();toast('已移除观察授权，原文件不受影响');}catch(error){toast(adapter.mode==='desktop'?desktopError('移除根目录',error):'暂时无法移除授权');}}
