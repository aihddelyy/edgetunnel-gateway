import { cleanIdleH2Sessions } from './proxy-xhttp.mjs';
import { cleanStalePreferNodes } from './prefer.mjs';
import { cleanExpiredSessions } from './auth.mjs';
import { cleanStaleQuotaAlerts } from './cf-quota.mjs';
import { currentConfig } from './config.mjs';

let memoryGuardTimer = null;
let pruneLogBufferFn = null;

export function registerLogPruner(pruner) {
  pruneLogBufferFn = pruner;
}

/**
 * 获取详细的系统内存指标
 */
export function getMemoryStats() {
  const usage = process.memoryUsage();
  const rssMB = +(usage.rss / 1024 / 1024).toFixed(1);
  const heapTotalMB = +(usage.heapTotal / 1024 / 1024).toFixed(1);
  const heapUsedMB = +(usage.heapUsed / 1024 / 1024).toFixed(1);
  const externalMB = +(usage.external / 1024 / 1024).toFixed(1);
  const arrayBuffersMB = +(usage.arrayBuffers / 1024 / 1024).toFixed(1);
  const heapPercent = heapTotalMB > 0 ? +((heapUsedMB / heapTotalMB) * 100).toFixed(1) : 0;
  const isGcExposed = typeof global.gc === 'function';

  return {
    rssMB,
    heapTotalMB,
    heapUsedMB,
    externalMB,
    arrayBuffersMB,
    heapPercent,
    isGcExposed
  };
}

/**
 * 深度清洗各业务模块内的闲置长连接与陈旧缓存
 */
export function cleanSystemCaches() {
  const results = {
    closedH2Sessions: 0,
    cleanedPreferNodes: 0,
    cleanedSessions: 0,
    cleanedQuotaAlerts: 0,
    prunedLogs: 0
  };

  try {
    results.closedH2Sessions = cleanIdleH2Sessions(true);
  } catch (e) {}

  try {
    results.cleanedPreferNodes = cleanStalePreferNodes();
  } catch (e) {}

  try {
    results.cleanedSessions = cleanExpiredSessions();
  } catch (e) {}

  try {
    results.cleanedQuotaAlerts = cleanStaleQuotaAlerts();
  } catch (e) {}

  if (typeof pruneLogBufferFn === 'function') {
    try {
      results.prunedLogs = pruneLogBufferFn();
    } catch (e) {}
  }

  return results;
}

/**
 * 执行系统深度垃圾回收与内存释放
 * @param {string} reason 触发原因 (manual / auto-patrol / threshold)
 */
export function performGarbageCollection(reason = 'manual') {
  const before = getMemoryStats();

  // 1. 深度清理各模块内部闲置连接与过期缓存
  const cacheCleanResult = cleanSystemCaches();

  // 2. 触发 V8 引擎原生 Garbage Collection (需启动带 --expose-gc)
  let gcExecuted = false;
  if (typeof global.gc === 'function') {
    try {
      global.gc(); // 主要 Major GC
      global.gc(); // 二次调用彻底紧凑堆碎片
      gcExecuted = true;
    } catch (err) {
      console.warn(`[Memory Guard] 触发 V8 GC 异常: ${err.message}`);
    }
  }

  const after = getMemoryStats();
  const freedRssMB = +(before.rssMB - after.rssMB).toFixed(1);
  const freedHeapMB = +(before.heapUsedMB - after.heapUsedMB).toFixed(1);

  const logMessage = `[Memory Guard] 深度内存回收完成 [来源: ${reason} | 原生V8 GC: ${gcExecuted ? '已执行' : '未暴露(建议启动参数加 --expose-gc)'}]: ` +
    `RSS: ${before.rssMB}MB -> ${after.rssMB}MB (释放 ${freedRssMB > 0 ? freedRssMB : 0}MB), ` +
    `Heap: ${before.heapUsedMB}MB -> ${after.heapUsedMB}MB (释放 ${freedHeapMB > 0 ? freedHeapMB : 0}MB)`;

  if (freedRssMB > 0 || freedHeapMB > 0 || gcExecuted) {
    console.log(logMessage);
  } else {
    console.log(`[Memory Guard] 内存处于极佳低位状态，当前常驻物理内存: ${after.rssMB}MB`);
  }

  return {
    ok: true,
    reason,
    gcExecuted,
    isGcExposed: before.isGcExposed,
    cacheCleanResult,
    before,
    after,
    freedRssMB: freedRssMB > 0 ? freedRssMB : 0,
    freedHeapMB: freedHeapMB > 0 ? freedHeapMB : 0
  };
}

/**
 * 启动后台智能内存巡检与自动回收守护器
 */
export function startMemoryGuard() {
  if (memoryGuardTimer) {
    clearInterval(memoryGuardTimer);
    memoryGuardTimer = null;
  }

  const mgCfg = currentConfig.memoryGuard || {};
  const enabled = mgCfg.enabled !== false;
  const thresholdMB = Math.max(50, parseInt(mgCfg.thresholdMB || '150', 10));
  const intervalMinutes = Math.max(1, parseInt(mgCfg.intervalMinutes || '5', 10));

  if (!enabled) {
    console.log('[Memory Guard] 后台内存自动回收巡检器已处于关闭状态');
    return;
  }

  console.log(`[Memory Guard] 内存自动回收守护已启动: 每 ${intervalMinutes} 分钟巡检，阈值 ${thresholdMB} MB (原生GC: ${typeof global.gc === 'function' ? '已就绪' : '未开启--expose-gc'})`);

  memoryGuardTimer = setInterval(() => {
    try {
      const stats = getMemoryStats();
      if (stats.rssMB >= thresholdMB || stats.heapUsedMB >= thresholdMB * 0.8) {
        console.warn(`[Memory Guard] ⚠️ 当前物理内存 (${stats.rssMB}MB) 超过设定阈值 (${thresholdMB}MB)，正在执行自动深度回收...`);
        performGarbageCollection('阈值自动守护');
      }
    } catch (err) {
      console.warn(`[Memory Guard] 内存巡检发生异常: ${err.message}`);
    }
  }, intervalMinutes * 60 * 1000);
}
