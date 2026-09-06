// 视图操作：平移/缩放/命中测试/聚焦分区。
import { state } from '../state/state.js';
import { draw, stage, canvas, screen } from './render.js';

export function hitTest(clientX, clientY) { const rect=canvas.getBoundingClientRect(), x=clientX-rect.left,y=clientY-rect.top; return [...state.nodes].reverse().find(n=>{const p=screen(n);return Math.hypot(x-p.x,y-p.y)<=p.radius*1.08;}); }

export function fitView() {
  if (!state.nodes.length) return; const maxX=Math.max(...state.nodes.map(n=>Math.abs(n.x)+n.r)), maxY=Math.max(...state.nodes.map(n=>Math.abs(n.y)+n.r));
  state.view.scale=Math.max(.42, Math.min(2.4, Math.min((stage.clientWidth-88)/(maxX*2),(stage.clientHeight-72)/(maxY*2)))); state.view.x=0;state.view.y=0;
}

export function zoom(factor,cx=stage.clientWidth/2,cy=stage.clientHeight/2){const old=state.view.scale,next=Math.max(.35,Math.min(2.6,old*factor));state.view.x=(state.view.x+stage.clientWidth/2-cx)*(next/old)-stage.clientWidth/2+cx;state.view.y=(state.view.y+stage.clientHeight/2-cy)*(next/old)-stage.clientHeight/2+cy;state.view.scale=next;draw();}

export function focusZone(zone) { const group=state.nodes.filter(n=>n.zone===zone);if(!group.length)return;state.view.x=-(group.reduce((s,n)=>s+n.x,0)/group.length)*state.view.scale;state.view.y=-(group.reduce((s,n)=>s+n.y,0)/group.length)*state.view.scale;draw(); }
