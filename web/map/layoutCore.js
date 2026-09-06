// 纯布局计算：不接触 DOM、不引用全局状态，因此既能在主线程回退运行，
// 也能在 Web Worker 里运行。aspect（舞台宽高比）由调用方传入。

// Sizes are areas, not radii: a file twice as large must look twice as big.
// The old formula bottomed out at a floor for anything under ~100 MB, which
// flattened every file into the same bead.
export function layoutNodes(nodes, aspect) {
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

// 稳定的伪随机抖动：同一条目每次布局得到同一个位置，地图不会在
// 每次重排时轻微跳动。渲染层的孢子姿态也复用它。
export function hash01(n) { const x = Math.sin(n * 127.1 + 311.7) * 43758.5453; return x - Math.floor(x); }
