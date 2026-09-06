// 栖境前端入口：只负责事件绑定与启动引导；业务逻辑分属各模块
// （api/ state/ components/ map/ scan/ agent/ recycle/ settings/ diagnostics/）。
import { state, zones } from './state/state.js';
import { $, escapeHtml, formatBytes } from './components/dom.js';
import { canvas, stage, resize, draw } from './map/render.js';
import { expandAggregate } from './map/nodes.js';
import { fitView, zoom, hitTest, focusZone } from './map/view.js';
import { search } from './map/sidebar.js';
import { openDetail, closeDrawer, drawerAction } from './map/detail.js';
import { startDemo } from './map/demo.js';
import { startScan, togglePause, bootstrap } from './scan/controller.js';
import { loadDrives, addRoot, chooseRoot, openComputerScan, confirmComputerScan, removeRoot, selectedDrives } from './scan/roots.js';
import { openAgentPreview, confirmAgentRun, cancelAgentRun } from './agent/run.js';
import { saveAgentSettings, testAgentConnection } from './agent/profile.js';
import { openRecycle, reviewRecycle, confirmRecycle, onRecycleListChange } from './recycle/recycle.js';
import { switchSettingsTab } from './settings/settings.js';
import { openPrivacy, switchAuditTab } from './diagnostics/audit.js';

canvas.addEventListener('pointerdown',e=>{state.dragging=true;state.moved=false;state.last={x:e.clientX,y:e.clientY};canvas.setPointerCapture(e.pointerId);canvas.classList.add('dragging');});
canvas.addEventListener('pointermove',e=>{if(state.dragging){const dx=e.clientX-state.last.x,dy=e.clientY-state.last.y;if(Math.abs(dx)+Math.abs(dy)>2)state.moved=true;state.view.x+=dx;state.view.y+=dy;state.last={x:e.clientX,y:e.clientY};draw();return;}const n=hitTest(e.clientX,e.clientY);state.hover=n?String(n.id):null;canvas.style.cursor=n?'pointer':'grab';const tip=$('#mapTooltip');if(n){tip.innerHTML=`<strong>${escapeHtml(n.name)}</strong><span class="tip-meta"><i class="tip-dot" style="background:${zones[n.zone].color}"></i>${zones[n.zone].name} · ${formatBytes(n.size)}${n.aggregate?' · 点击展开':''}</span>`;tip.hidden=false;const r=stage.getBoundingClientRect();tip.style.left=`${Math.min(e.clientX-r.left+14,r.width-220)}px`;tip.style.top=`${Math.min(e.clientY-r.top+14,r.height-72)}px`;}else tip.hidden=true;draw();});
canvas.addEventListener('pointerup',e=>{if(!state.moved){const n=hitTest(e.clientX,e.clientY);if(n){if(n.aggregate)expandAggregate(n.id);else openDetail(n);}}state.dragging=false;canvas.classList.remove('dragging');});
canvas.addEventListener('pointerleave',()=>{$('#mapTooltip').hidden=true;state.hover=null;draw();});
canvas.addEventListener('wheel',e=>{e.preventDefault();const r=stage.getBoundingClientRect();zoom(e.deltaY<0?1.12:.89,e.clientX-r.left,e.clientY-r.top);},{passive:false});

$('#zoomIn').onclick=()=>zoom(1.18);$('#zoomOut').onclick=()=>zoom(.84);$('#resetView').onclick=()=>{fitView();draw();};
$('#scanBtn').onclick=startScan;$('#pauseBtn').onclick=togglePause;$('#demoBtn').onclick=startDemo;$('#settingsBtn').onclick=()=>{$('#settingsDialog').showModal();loadDrives();};$('#emptyRootBtn').onclick=()=>{$('#settingsDialog').showModal();loadDrives();};$('#privacyBtn').onclick=openPrivacy;$('#addRootBtn').onclick=addRoot;$('#chooseRootBtn').onclick=chooseRoot;$('#computerScanBtn').onclick=openComputerScan;$('#confirmComputerScanBtn').onclick=confirmComputerScan;
$('#agentPatrolBtn').onclick=openAgentPreview;$('#confirmAgentBtn').onclick=confirmAgentRun;$('#cancelAgentBtn').onclick=cancelAgentRun;$('#closeAgentDrawer').onclick=()=>{ $('#agentDrawer').classList.remove('open');$('#agentDrawer').setAttribute('aria-hidden','true');$('#backdrop').hidden=true; };$('#saveAgentBtn').onclick=saveAgentSettings;$('#testAgentBtn').onclick=testAgentConnection;
$('.settings-tabs').onclick=e=>{const b=e.target.closest('[data-settings-tab]');if(b)switchSettingsTab(b.dataset.settingsTab);};$('.audit-tabs').onclick=e=>{const b=e.target.closest('[data-audit-tab]');if(b)switchAuditTab(b.dataset.auditTab);};
$('#networkEnabled').onchange=()=>{if(!$('#networkEnabled').checked&&state.agent.running)cancelAgentRun();};
$('#rootsList').onclick=e=>{const button=e.target.closest('[data-remove-root]');if(button)removeRoot(Number(button.dataset.removeRoot));};$('#driveList').onchange=e=>{const input=e.target.closest('[data-drive-index]');if(!input)return;const drive=state.drives[Number(input.dataset.driveIndex)];if(drive)drive.selected=input.checked;$('#computerScanBtn').disabled=!selectedDrives().length;};
$('#recycleBtn').onclick=openRecycle;$('#recycleReviewBtn').onclick=reviewRecycle;$('#confirmRecycleBtn').onclick=confirmRecycle;
$('#recycleList').onchange=e=>{const input=e.target.closest('[data-recycle-index]');if(input)onRecycleListChange(input);};
$('#zoneList').onclick=e=>{const b=e.target.closest('[data-zone]');if(b)focusZone(b.dataset.zone);};$('#briefCards').onclick=e=>{const b=e.target.closest('[data-zone]');if(b)focusZone(b.dataset.zone);};
$('.drawer-close').onclick=closeDrawer;$('#backdrop').onclick=()=>{closeDrawer();$('#agentDrawer').classList.remove('open');$('#agentDrawer').setAttribute('aria-hidden','true');};$('#drawerContent').onclick=e=>{const b=e.target.closest('[data-action]');if(b)drawerAction(b.dataset.action);};
$('#searchInput').addEventListener('input',e=>search(e.target.value));$('#searchResults').onclick=e=>{const b=e.target.closest('[data-node]');if(!b)return;const n=state.allNodes.find(n=>String(n.id)===b.dataset.node);if(n){if(!state.nodes.includes(n))expandAggregate(`agg:${n.zone}`);openDetail(n);}$('#searchResults').hidden=true;};
document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();$('#searchInput').focus();$('#searchInput').select();}if(e.key==='Escape')closeDrawer();});
new ResizeObserver(resize).observe(stage);
bootstrap();
