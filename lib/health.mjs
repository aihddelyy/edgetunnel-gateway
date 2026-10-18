import { currentConfig } from './config.mjs';
import { sendTelegramAlert } from './telegram.mjs';

export const backendStats = new Map();
export const backendHealth = new Map();
export const backendFailureCounts = new Map();

export const CIRCUIT_BREAKER_TTL_MS = 60 * 1000;
export const FAILURE_THRESHOLD = 5;

export function recordBackendReq(backend, isSuccess = true) {
  if (!backend) return;
  let s = backendStats.get(backend);
  if (!s) {
    s = { total: 0, success: 0, fail: 0 };
    backendStats.set(backend, s);
  }
  s.total++;
  if (isSuccess) s.success++;
  else s.fail++;
}

export function isBackendDisabled(backend) {
  return Array.isArray(currentConfig.disabledBackends) && currentConfig.disabledBackends.includes(backend);
}

export function markBackendSuccess(backend) {
  recordBackendReq(backend, true);
  backendFailureCounts.set(backend, 0);
  const state = backendHealth.get(backend);
  if (state && !state.isHealthy) {
    console.log(`\x1b[32m[Health Check] 后端 ${backend} 传输成功，熔断解除，静默恢复健康池！\x1b[0m`);
    backendHealth.delete(backend);
  }
}

export function isNetworkTransportError(reason) {
  if (!reason) return false;
  return /ETIMEDOUT|ECONNREFUSED|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|timeout|超时|ECONNRESET|EPIPE|CANCEL|stream.*destroyed|Client aborted|NGHTTP2_REFUSED_STREAM/i.test(reason);
}

export function markBackendFailure(backend, reason, metrics = null) {
  // 严格过滤：若是优选链路丢包、超时或网络层异常，属于客户端优选 IP 的问题，严禁扣减 Worker 成功率与触发误熔断
  if (!backend || isNetworkTransportError(reason) || (typeof reason === 'string' && reason.includes('Invalid URL'))) {
    return;
  }

  // 只有明确属于 Cloudflare 边缘阻断错误（如 1101、502、429 等）才判定为真实业务失败
  recordBackendReq(backend, false);

  const now = Date.now();
  const currentFails = (backendFailureCounts.get(backend) || 0) + 1;
  backendFailureCounts.set(backend, currentFails);

  const existing = backendHealth.get(backend);
  if (existing && !existing.isHealthy) return;

  if (currentFails >= FAILURE_THRESHOLD) {
    if (metrics) metrics.circuitTrips++;
    console.warn(`\x1b[33m[Circuit Breaker] ⚠️ 熔断告警: 后端 ${backend} 连续遭遇 ${currentFails} 次明确阻断 [${reason}]，自动隔离 ${CIRCUIT_BREAKER_TTL_MS / 1000} 秒。\x1b[0m`);
    backendHealth.set(backend, {
      isHealthy: false,
      blockUntil: now + CIRCUIT_BREAKER_TTL_MS,
      reason: `连续 ${currentFails} 次失败: ${reason}`
    });
    sendTelegramAlert(backend, reason, metrics);
  } else {
    console.warn(`[Health Check] ⚠️ 后端 ${backend} 遇到异常 [${reason}] (连续失败 ${currentFails}/${FAILURE_THRESHOLD} 次，暂不熔断)`);
  }
}

export function resetAllCircuit() {
  backendHealth.clear();
  backendFailureCounts.clear();
  console.log('[Circuit Breaker] 所有后端熔断状态已被手动重置');
}

export function getHealthyBackends(list, excludeBackends = [], cachedCfAccounts = null) {
  const now = Date.now();
  const excludes = Array.isArray(excludeBackends) ? excludeBackends : [excludeBackends].filter(Boolean);

  const enabledList = list.filter(b => !isBackendDisabled(b) && !excludes.includes(b));
  const baseList = enabledList.length > 0 ? enabledList : list.filter(b => !excludes.includes(b));
  if (baseList.length === 0) return list;

  const unblocked = baseList.filter(b => {
    const s = backendHealth.get(b);
    if (!s) return true;
    if (!s.isHealthy && now > s.blockUntil) {
      console.log(`[Circuit Breaker] ⏱️ 后端 ${b} 隔离期已满，重新加入轮询探活。`);
      backendHealth.delete(b);
      return true;
    }
    return s.isHealthy;
  });

  const candidateList = unblocked.length > 0 ? unblocked : baseList;

  const map = currentConfig.backendAccountMap || {};
  const cfAccounts = cachedCfAccounts?.accounts || [];
  if (cfAccounts.length > 0) {
    const safeFromQuota = candidateList.filter(b => {
      const accId = map[b];
      if (!accId) return true;
      const acc = cfAccounts.find(a => (String(a.id) === String(accId) || String(a.accountId) === String(accId) || String(a.name) === String(accId)));
      if (!acc) return true;
      const pct = acc.percent !== undefined ? acc.percent : (acc.total / (acc.max || 100000) * 100);
      const avoidThreshold = Math.max(1, Math.min(100, Number(currentConfig.cfQuotaAvoidThreshold !== undefined ? currentConfig.cfQuotaAvoidThreshold : 95)));
      return pct < avoidThreshold;
    });
    if (safeFromQuota.length > 0) {
      return safeFromQuota;
    }
  }

  return candidateList;
}

let pagesIndex = 0;
let workerIndex = 0;

export const stickyWorkers = new Map(); // clientTag (ip:port) -> { backend, lastActiveTime }
export const stickyPages = new Map();   // clientTag (ip:port) -> { backend, lastActiveTime }
const MAX_STICKY_ENTRIES = 2000;

function trimStickyMap(map) {
  if (map.size <= MAX_STICKY_ENTRIES) return;
  // 超过容量上限时淘汰最旧的一半条目，防止极端高频连接积压内存
  const entries = Array.from(map.entries()).sort((a, b) => a[1].lastActiveTime - b[1].lastActiveTime);
  const deleteCount = Math.floor(entries.length / 2);
  for (let i = 0; i < deleteCount; i++) {
    map.delete(entries[i][0]);
  }
}

export function cleanExpiredStickySessions(now = Date.now()) {
  const ttl = (currentConfig.stickyDurationSec || 60) * 1000;
  for (const [tag, item] of stickyWorkers.entries()) {
    if (now - item.lastActiveTime > ttl) {
      stickyWorkers.delete(tag);
    }
  }
  for (const [tag, item] of stickyPages.entries()) {
    if (now - item.lastActiveTime > ttl) {
      stickyPages.delete(tag);
    }
  }
}

export function resetStickySessions() {
  const count = stickyWorkers.size + stickyPages.size;
  stickyWorkers.clear();
  stickyPages.clear();
  return count;
}

setInterval(() => {
  cleanExpiredStickySessions();
}, 30000);

export function getNextPagesBackend(excludeBackends = [], cachedCfAccounts = null, clientTag = '') {
  const full = currentConfig.pagesBackends.length > 0 ? currentConfig.pagesBackends : currentConfig.workerBackends;
  const active = getHealthyBackends(full, excludeBackends, cachedCfAccounts);
  if (!active || active.length === 0) return null;

  const mode = currentConfig.loadBalanceMode || 'sticky';
  const ttl = (currentConfig.stickyDurationSec || 60) * 1000;
  const now = Date.now();

  // 若处于会话粘性模式且非故障转移重试：以 clientTag (IP:Port) 为粒度锁定
  // 同一连接会话持续复用同一后端保持速度，自适应软路由不同连接均匀轮询
  if (mode === 'sticky' && clientTag && (!excludeBackends || excludeBackends.length === 0)) {
    const existing = stickyPages.get(clientTag);
    if (existing && active.includes(existing.backend)) {
      if (now - existing.lastActiveTime <= ttl) {
        existing.lastActiveTime = now;
        return existing.backend;
      }
    }
  }

  const target = active[pagesIndex % active.length];
  pagesIndex = (pagesIndex + 1) % 1000000;

  if (mode === 'sticky' && clientTag) {
    trimStickyMap(stickyPages);
    stickyPages.set(clientTag, { backend: target, lastActiveTime: now });
  }

  return target;
}

export function getNextWorkerBackend(excludeBackends = [], cachedCfAccounts = null, clientTag = '') {
  const full = currentConfig.workerBackends.length > 0 ? currentConfig.workerBackends : currentConfig.pagesBackends;
  const active = getHealthyBackends(full, excludeBackends, cachedCfAccounts);
  if (!active || active.length === 0) return null;

  const mode = currentConfig.loadBalanceMode || 'sticky';
  const ttl = (currentConfig.stickyDurationSec || 60) * 1000;
  const now = Date.now();

  // 若处于会话粘性模式且非故障转移重试：以 clientTag (IP:Port) 为粒度锁定，保持 TCP/H2 热连接拉满速度
  // 自适应软路由环境：不同代理连接拥有独立端口，自动均匀分摊至各个 Worker
  if (mode === 'sticky' && clientTag && (!excludeBackends || excludeBackends.length === 0)) {
    const existing = stickyWorkers.get(clientTag);
    if (existing && active.includes(existing.backend)) {
      if (now - existing.lastActiveTime <= ttl) {
        existing.lastActiveTime = now;
        return existing.backend;
      }
    }
  }

  const target = active[workerIndex % active.length];
  workerIndex = (workerIndex + 1) % 1000000;

  if (mode === 'sticky' && clientTag) {
    trimStickyMap(stickyWorkers);
    stickyWorkers.set(clientTag, { backend: target, lastActiveTime: now });
  }

  return target;
}

