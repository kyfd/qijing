// Agent 模型配置：Provider、Base URL、模型名、联网开关与 API Key。
// API Key 只提交给本地服务保存（DPAPI 密文），界面从不回显。
import { state } from '../state/state.js';
import { $, toast } from '../components/dom.js';
import { adapter } from '../api/adapter.js';

export function normalizeProfile(data = {}) {
  const profile = data.profile || data;
  return { provider_type: profile.provider || profile.provider_type || profile.type || 'cloud', base_url: profile.base_url || profile.baseURL || '', model: profile.model || profile.model_name || '', network_enabled: Boolean(profile.network_enabled ?? profile.network), has_api_key: Boolean(profile.has_api_key ?? profile.api_key_configured) };
}

export function updateTrustStrip() {
  const local = state.model.provider_type === 'local';
  $('#networkTrust').innerHTML = `<i class="cloud-mini"></i>${state.model.network_enabled ? '模型联网开启' : '模型联网关闭'}`;
  $('#networkTrust').classList.toggle('online', Boolean(state.model.network_enabled));
  $('#providerTrust').textContent = local ? '本地模型 · 数据不离机' : (state.model.model ? `云端模型 · ${state.model.model}` : '云端模型未配置');
  $('.agent-note p').innerHTML = state.model.network_enabled && !local ? '<strong>云端 Agent 已由你启用</strong><br>仅在逐次确认后发送匿名 payload。' : (local ? '<strong>本地模型待命</strong><br>Payload 仍会在运行前由你确认。' : '<strong>Agent 正在本地待命</strong><br>联网关闭，不会向外部模型发送数据。');
}

export async function loadModelProfile() {
  try { state.model = normalizeProfile(await adapter.getModelProfile()); } catch (_) { state.model = normalizeProfile(state.model); }
  $('#agentBaseUrl').value = state.model.base_url;
  $('#agentModel').value = state.model.model;
  $('#networkEnabled').checked = state.model.network_enabled;
  const radio = document.querySelector(`input[name="providerType"][value="${state.model.provider_type}"]`);
  if (radio) radio.checked = true;
  $('#agentApiKey').placeholder = state.model.has_api_key ? '已安全保存 · 留空保持不变' : 'sk-••••••••';
  updateTrustStrip();
}

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

export function explainAgentError(error) {
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

export async function saveAgentSettings() {
  const status = $('#agentSettingsStatus');
  status.textContent = '正在保存到本地安全存储…';
  try {
    await persistAgentSettings();
    status.textContent = '设置已保存在本机。';
    toast('Agent 设置已保存');
  } catch (error) { status.textContent = `保存失败：${explainAgentError(error)}`; }
}

export async function testAgentConnection() {
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
