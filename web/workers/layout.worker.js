// 布局 Worker：把圆簇松弛计算挪出主线程，几百个节点重排时 UI 不再掉帧。
// 消息协议：{ id, nodes, aspect } 进，{ id, nodes } 出；nodes 是纯数据对象，
// 结构化克隆负责往返。任何一次计算失败都会把错误带回，由调用方降级。
import { layoutNodes } from '../map/layoutCore.js';

self.onmessage = (e) => {
  const { id, nodes, aspect } = e.data;
  try {
    layoutNodes(nodes, aspect);
    self.postMessage({ id, nodes, error: null });
  } catch (err) {
    self.postMessage({ id, nodes: null, error: String(err && err.message || err) });
  }
};
