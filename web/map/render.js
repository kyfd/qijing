// Canvas 渲染：孢子形态、光晕、遮挡避让的字幕与动画循环。
// 只读 state 中的节点与视图状态；布局交给 layout 模块。
import { state, zones, reduceMotion } from '../state/state.js';
import { $, formatBytes } from '../components/dom.js';
import { hash01 } from './layoutCore.js';

export const canvas = $('#ecosystemCanvas');
export const stage = $('#canvasStage');
export const ctx = canvas.getContext('2d');

function rgbOf(hex) { const n = parseInt(hex.slice(1), 16); return [n >> 16, (n >> 8) & 255, n & 255]; }
function rgba(rgb, a) { return `rgba(${rgb[0]},${rgb[1]},${rgb[2]},${a})`; }
function lift(rgb, n) { return [Math.min(255, rgb[0] + n), Math.min(255, rgb[1] + n), Math.min(255, rgb[2] + n)]; }
function dim(rgb, k) { return [rgb[0] * k | 0, rgb[1] * k | 0, rgb[2] * k | 0]; }

export function screen(node) { const r = stage.getBoundingClientRect(); return { x:r.width/2 + state.view.x + node.x*state.view.scale, y:r.height/2 + state.view.y + node.y*state.view.scale, radius:node.r*state.view.scale }; }

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

export function ensureAnim() {
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

export function stopAnim() {
  if (state.anim) cancelAnimationFrame(state.anim);
  state.anim = 0;
}

export function resize() {
  const rect = stage.getBoundingClientRect(), dpr = Math.min(devicePixelRatio || 1, 2);
  canvas.width = Math.max(1, rect.width * dpr); canvas.height = Math.max(1, rect.height * dpr);
  canvas.style.width = `${rect.width}px`; canvas.style.height = `${rect.height}px`; ctx.setTransform(dpr,0,0,dpr,0,0); draw();
}

export function draw() {
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
