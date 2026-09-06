// 与框架无关的 DOM 与格式化工具。所有展示模块从这里取格式化函数，
// 保证字节、条数与时长在整份界面里以同一种方式呈现。
export const $ = (selector) => document.querySelector(selector);

export function escapeHtml(value){const el=document.createElement('span');el.textContent=String(value);return el.innerHTML;}

export function toast(message){const el=document.createElement('div');el.className='toast';el.textContent=message;$('#toastRegion').appendChild(el);setTimeout(()=>el.remove(),2800);}

export function formatBytes(bytes = 0) {
  if (!bytes) return '0 B';
  const units = ['B','KB','MB','GB','TB']; let i = 0, value = Number(bytes);
  while (value >= 1024 && i < units.length - 1) { value /= 1024; i++; }
  return `${value >= 10 || i === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[i]}`;
}

export function firstValue(...values) { return values.find(value => value !== undefined && value !== null && value !== ''); }

export function numeric(...values) { const value = Number(firstValue(...values)); return Number.isFinite(value) && value >= 0 ? value : 0; }

export function formatCount(value) { return Math.floor(numeric(value)).toLocaleString('zh-CN'); }

export function formatDuration(ms) {
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const seconds = Math.floor(ms / 1000), hours = Math.floor(seconds / 3600), minutes = Math.floor((seconds % 3600) / 60), rest = seconds % 60;
  return hours ? `${hours}:${String(minutes).padStart(2,'0')}:${String(rest).padStart(2,'0')}` : `${minutes}:${String(rest).padStart(2,'0')}`;
}

// renderMarkdown 只支持 Agent 报告实际用到的子集（标题、列表、行内强调），
// 全部内容先经 escapeHtml 再加标记，报告正文永远不会注入 HTML。
export function renderMarkdown(src) {
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
