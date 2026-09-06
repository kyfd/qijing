// 设置对话框的页签切换。具体设置内容分属各自模块（授权在 scan/roots，
// Agent 配置在 agent/profile）。
import { $ } from '../components/dom.js';

export function switchSettingsTab(name){document.querySelectorAll('[data-settings-tab]').forEach(b=>b.classList.toggle('active',b.dataset.settingsTab===name));$('#rootsSettings').hidden=name!=='roots';$('#agentSettings').hidden=name!=='agent';}
