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

export function getNextPagesBackend(excludeBackends = [], cachedCfAccounts = null) {
  const full = currentConfig.pagesBackends.length > 0 ? currentConfig.pagesBackends : currentConfig.workerBackends;
  const active = getHealthyBackends(full, excludeBackends, cachedCfAccounts);
  if (!active || active.length === 0) return null;

  const target = active[pagesIndex % active.length];
  pagesIndex = (pagesIndex + 1) % 1000000;
  return target;
}

export function getNextWorkerBackend(excludeBackends = [], cachedCfAccounts = null) {
  const full = currentConfig.workerBackends.length > 0 ? currentConfig.workerBackends : currentConfig.pagesBackends;
  const active = getHealthyBackends(full, excludeBackends, cachedCfAccounts);
  if (!active || active.length === 0) return null;

  const target = active[workerIndex % active.length];
  workerIndex = (workerIndex + 1) % 1000000;
  return target;
}

