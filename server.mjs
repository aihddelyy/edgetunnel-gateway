import http from 'node:http';
import https from 'node:https';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { EventEmitter } from 'node:events';

import {
  CONFIG_FILE,
  PUBLIC_HTML_PATH,
  currentConfig,
  loadConfigFromFile,
  saveConfigToFile,
  getBackupFilesList,
  rollbackConfigFile,
  cleanTelegramChatId
} from './lib/config.mjs';

import { authSessions, hashPassword, checkAuth, createSession } from './lib/auth.mjs';
import { sendTelegramRequest, verifyTelegramConnectivity } from './lib/telegram.mjs';
import { backendHealth, backendStats, resetAllCircuit } from './lib/health.mjs';
import { recordPreferActivity, getPreferNodesList, pingPreferTarget, normalizePreferTarget, preferNodes } from './lib/prefer.mjs';
import { trafficHistory, initTrafficBaseline, startTrafficSampler } from './lib/traffic.mjs';
import { cachedCfAccounts, lastCfAccountsFetch, startQuotaPatrol, getCfSubAccounts, getCfDirectAccounts } from './lib/cf-quota.mjs';
import { isBenignError } from './lib/utils.mjs';
import { createWsServer } from './lib/proxy-ws.mjs';
import { createXhttpGateway } from './lib/proxy-xhttp.mjs';
import {
  getMemoryStats,
  performGarbageCollection,
  startMemoryGuard,
  registerLogPruner
} from './lib/memory.mjs';

// ==========================================
// 1. 全局指标与日志收集系统
// ==========================================
const metrics = {
  startTime: Date.now(),
  wsRequests: 0,
  xhttpRequests: 0,
  h2cRequests: 0,
  h1Requests: 0,
  alertsSent: 0,
  circuitTrips: 0
};

const logEmitter = new EventEmitter();
logEmitter.setMaxListeners(100);
const MAX_LOG_BUFFER = 250;
const logBuffer = [];

function appendLog(level, rawMsg) {
  const logItem = {
    id: Date.now() + Math.random(),
    time: new Date().toLocaleTimeString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }),
    level,
    message: rawMsg
  };
  logBuffer.push(logItem);
  if (logBuffer.length > MAX_LOG_BUFFER) logBuffer.shift();
  logEmitter.emit('log', logItem);
}

const origLog = console.log;
const origError = console.error;
const origWarn = console.warn;

console.log = (...args) => {
  origLog(...args);
  appendLog('info', args.map(a => (typeof a === 'object' ? JSON.stringify(a) : String(a))).join(' '));
};
console.error = (...args) => {
  origError(...args);
  appendLog('error', args.map(a => (typeof a === 'object' ? JSON.stringify(a) : String(a))).join(' '));
};
console.warn = (...args) => {
  origWarn(...args);
  appendLog('warn', args.map(a => (typeof a === 'object' ? JSON.stringify(a) : String(a))).join(' '));
};

registerLogPruner(() => {
  let pruned = 0;
  if (logBuffer.length > 100) {
    pruned = logBuffer.length - 100;
    logBuffer.splice(0, pruned);
  }
  return pruned;
});

// 初始化配置与历史采样基线
loadConfigFromFile();
initTrafficBaseline();
startTrafficSampler(metrics);
startMemoryGuard();

// ==========================================
// 2. 静态页面与通用静态资源托管
// ==========================================
function getWebUiHtml() {
  if (fs.existsSync(PUBLIC_HTML_PATH)) {
    return fs.readFileSync(PUBLIC_HTML_PATH, 'utf8');
  }
  return `<!DOCTYPE html><html><body style="font-family:sans-serif;background:#0b0f19;color:#fff;padding:2rem;">
    <h2>⚡ EdgeTunnel LB 管理控制台</h2>
    <p style="color:#f87171;">⚠️ 静态文件未找到: <code>${PUBLIC_HTML_PATH}</code></p>
    <p>请确保 <code>public/index.html</code> 存在于挂载目录下。</p>
  </body></html>`;
}

// ==========================================
// 3. Web UI 管理面板与 REST API (端口 8789)
// ==========================================
const webServer = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host || 'localhost'}`);
  const pathname = url.pathname;

  // 1. 通用静态资源托管 (支持 index.html, /css/*, /js/* 等公共静态资源)
  if (pathname === '/' || pathname === '/index.html' || pathname.startsWith('/css/') || pathname.startsWith('/js/') || pathname === '/favicon.ico') {
    const relPath = pathname === '/' ? 'index.html' : pathname.replace(/^\/+/, '');
    const safePath = path.resolve(process.cwd(), 'public', relPath);
    const publicDir = path.resolve(process.cwd(), 'public');

    if (safePath.startsWith(publicDir) && fs.existsSync(safePath) && fs.statSync(safePath).isFile()) {
      const ext = path.extname(safePath).toLowerCase();
      const mimeTypes = {
        '.html': 'text/html; charset=utf-8',
        '.css': 'text/css; charset=utf-8',
        '.js': 'application/javascript; charset=utf-8',
        '.json': 'application/json; charset=utf-8',
        '.svg': 'image/svg+xml',
        '.ico': 'image/x-icon',
        '.png': 'image/png'
      };
      const contentType = mimeTypes[ext] || 'application/octet-stream';
      res.writeHead(200, {
        'Content-Type': contentType,
        'Cache-Control': ext === '.html' ? 'no-cache, no-store, must-revalidate' : 'public, max-age=3600'
      });
      fs.createReadStream(safePath).pipe(res);
      return;
    } else if (pathname === '/' || pathname === '/index.html') {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end(getWebUiHtml());
      return;
    }
  }

  // ================= CF-Workers-UsagePanel 同源反向代理 =================
  if (pathname === '/api/cf-panel') {
    res.writeHead(302, { 'Location': '/api/cf-panel/' });
    res.end();
    return;
  }

  if (pathname === '/api/cf-panel/target' && req.method === 'GET') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ target: currentConfig.cfPanelUrl || 'https://usagepanel.pages.dev' }));
    return;
  }

  if (pathname === '/api/cf-panel/target' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const { target } = JSON.parse(body);
        if (!target) {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: '请提供有效的面板目标地址' }));
          return;
        }
        currentConfig.cfPanelUrl = target.trim();
        saveConfigToFile(currentConfig);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, target: currentConfig.cfPanelUrl }));
      } catch (e) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: '无效请求格式' }));
      }
    });
    return;
  }

  // 查询各 Cloudflare 子账户额度
  if (pathname === '/api/cf-panel/accounts' && req.method === 'GET') {
    const force = url.searchParams.get('force') === '1';
    const result = await getCfSubAccounts(force);
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(result));
    return;
  }

  // 配置并验证 Cloudflare 额度凭据
  if (pathname === '/api/cf-config' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', async () => {
      try {
        const payload = JSON.parse(body);
        const mode = payload.mode || 'usagepanel';
        const alertThreshold = parseInt(payload.alertThreshold || '80', 10);
        currentConfig.cfQuotaMode = mode;
        currentConfig.cfAlertThreshold = alertThreshold;
        if (payload.cfQuotaIntervalMinutes !== undefined) {
          const qm = parseInt(payload.cfQuotaIntervalMinutes, 10);
          if (!isNaN(qm) && qm >= 1) currentConfig.cfQuotaIntervalMinutes = qm;
        }

        if (mode === 'direct') {
          const directAccounts = payload.direct?.accounts || [];
          const prevAccounts = currentConfig.cfDirect?.accounts || [];
          const updatedAccounts = directAccounts.map((acc, idx) => {
            let token = (acc.apiToken || '').trim();
            if (!token) {
              const prev = prevAccounts.find(p => p.accountId === acc.accountId || p.id === acc.id) || prevAccounts[idx];
              if (prev && prev.apiToken) token = prev.apiToken;
            }
            return {
              id: acc.id || ('acc_' + idx),
              name: (acc.name || '').trim(),
              accountId: (acc.accountId || '').trim(),
              apiToken: token
            };
          });

          currentConfig.cfDirect = { accounts: updatedAccounts };
          saveConfigToFile(currentConfig);
          startQuotaPatrol();

          const testRes = await getCfDirectAccounts(true);
          console.log('[CF Direct API] 凭据已保存！直连账户数: ' + (testRes.count || updatedAccounts.length));
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: true, message: 'CF 官方直连凭据保存成功！', data: testRes }));
          return;
        } else {
          const newUrl = (payload.url || currentConfig.cfPanel?.url || 'https://usagepanel.pages.dev').trim();
          const newUser = (payload.username || '').trim();
          const newPass = payload.password !== undefined && payload.password !== '' ? payload.password : (currentConfig.cfPanel?.password || '');

          if (!newUser || !newPass) {
            res.writeHead(400, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ ok: false, error: '请提供面板管理员账号与密码' }));
            return;
          }

          currentConfig.cfPanel = {
            url: newUrl,
            username: newUser,
            password: newPass,
            alertThreshold
          };
          currentConfig.cfPanelUrl = newUrl;
          saveConfigToFile(currentConfig);
          startQuotaPatrol();

          const testRes = await getCfSubAccounts(true);
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: true, message: 'CF 面板凭据验证成功！', data: testRes }));
          return;
        }
      } catch (e) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: e.message }));
      }
    });
    return;
  }

  // 配置并验证 CF 面板凭据 (兼容旧接口)
  if (pathname === '/api/cf-panel/credentials' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', async () => {
      try {
        const payload = JSON.parse(body);
        const newUrl = (payload.url || currentConfig.cfPanel?.url || 'https://usagepanel.pages.dev').trim();
        const newUser = (payload.username || '').trim();
        const newPass = payload.password !== undefined && payload.password !== '' ? payload.password : (currentConfig.cfPanel?.password || '');
        const newThreshold = parseInt(payload.alertThreshold || '80', 10);

        if (!newUser || !newPass) {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '请提供面板管理员账号与密码' }));
          return;
        }

        const prevConfig = { ...currentConfig.cfPanel };
        currentConfig.cfPanel = {
          url: newUrl,
          username: newUser,
          password: newPass,
          alertThreshold: newThreshold
        };

        const testRes = await getCfSubAccounts(true);
        if (!testRes.ok) {
          currentConfig.cfPanel = prevConfig;
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '验证失败: ' + (testRes.error || testRes.message) }));
          return;
        }

        currentConfig.cfPanelUrl = newUrl;
        saveConfigToFile(currentConfig);
        startQuotaPatrol();
        console.log(`[CF Credentials] 验证通过并已持久化保存！账户数: ${testRes.accounts?.length}`);

        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, message: 'CF 面板凭据验证成功！', data: testRes }));
      } catch (e) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: e.message }));
      }
    });
    return;
  }

  if (pathname.startsWith('/api/cf-panel/') && pathname !== '/api/cf-panel/target' && pathname !== '/api/cf-panel/accounts' && pathname !== '/api/cf-panel/credentials') {
    const targetBase = (currentConfig.cfPanelUrl || 'https://usagepanel.pages.dev').replace(/\/+$/, '');
    let targetUrlObj;
    try {
      targetUrlObj = new URL(targetBase);
    } catch {
      targetUrlObj = new URL('https://usagepanel.pages.dev');
    }

    const subPath = pathname.replace(/^\/api\/cf-panel/, '') || '/';
    const remoteUrl = `${targetUrlObj.protocol}//${targetUrlObj.host}${subPath}${url.search}`;

    const headers = { ...req.headers };
    headers['host'] = targetUrlObj.host;
    delete headers['accept-encoding'];

    const clientCookie = req.headers['cookie'];
    if (clientCookie) headers['cookie'] = clientCookie;

    const proxyReq = https.request(remoteUrl, {
      method: req.method,
      headers,
      family: 4,
      timeout: 15000
    }, (proxyRes) => {
      const respHeaders = { ...proxyRes.headers };
      delete respHeaders['content-security-policy'];
      delete respHeaders['x-frame-options'];

      if (respHeaders.location) {
        const loc = respHeaders.location;
        if (loc.startsWith('/')) {
          respHeaders.location = '/api/cf-panel' + loc;
        } else {
          try {
            const locObj = new URL(loc);
            respHeaders.location = '/api/cf-panel' + locObj.pathname + locObj.search;
          } catch {}
        }
      }

      if (respHeaders['set-cookie']) {
        const cookies = Array.isArray(respHeaders['set-cookie'])
          ? respHeaders['set-cookie']
          : [respHeaders['set-cookie']];
        respHeaders['set-cookie'] = cookies.map(c => {
          return c
            .replace(/;\s*Secure/gi, '')
            .replace(/;\s*SameSite=Strict/gi, '; SameSite=Lax')
            .replace(/Path=[^;]+/gi, 'Path=/api/cf-panel');
        });
      }

      res.writeHead(proxyRes.statusCode, respHeaders);
      proxyRes.pipe(res);
    });

    proxyReq.on('error', (err) => {
      res.writeHead(502, { 'Content-Type': 'application/json; charset=utf-8' });
      res.end(JSON.stringify({ error: 'CF 额度面板连接失败: ' + err.message }));
    });

    req.pipe(proxyReq);
    return;
  }

  // ================= 认证相关 API =================
  if (pathname === '/api/auth/status' && req.method === 'GET') {
    const hasPassword = !!(currentConfig.auth && currentConfig.auth.passwordHash);
    const isAuth = checkAuth(req);
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({
      needSetup: !hasPassword,
      authenticated: hasPassword ? isAuth : false,
      username: (currentConfig.auth && currentConfig.auth.username) || 'admin'
    }));
    return;
  }

  if (pathname === '/api/auth/setup' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const { username, password } = JSON.parse(body);
        if (!password || password.length < 4) {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '密码长度至少需 4 位' }));
          return;
        }
        if (currentConfig.auth && currentConfig.auth.passwordHash) {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '系统已初始化过密码，请直接登录' }));
          return;
        }
        const finalUser = (username || 'admin').trim();
        const salt = crypto.randomBytes(16).toString('hex');
        const passwordHash = hashPassword(password, salt);
        currentConfig.auth = {
          enabled: true,
          username: finalUser,
          passwordHash,
          salt
        };
        saveConfigToFile(currentConfig);
        const token = createSession(finalUser, 7);
        console.log(`[Web Auth] 管理员账号首次初始化成功: ${finalUser}`);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, token, username: finalUser }));
      } catch (e) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: e.message }));
      }
    });
    return;
  }

  if (pathname === '/api/auth/login' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const { username, password } = JSON.parse(body);
        const auth = currentConfig.auth;
        if (!auth || !auth.passwordHash) {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '系统尚未初始化密码' }));
          return;
        }
        if (username !== auth.username) {
          res.writeHead(401, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '用户名或密码错误' }));
          return;
        }
        const hash = hashPassword(password, auth.salt);
        if (hash !== auth.passwordHash) {
          res.writeHead(401, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '用户名或密码错误' }));
          return;
        }
        const token = createSession(auth.username, 7);
        console.log(`[Web Auth] 管理员成功登录: ${auth.username}`);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, token, username: auth.username }));
      } catch (e) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: e.message }));
      }
    });
    return;
  }

  if (pathname === '/api/auth/logout' && req.method === 'POST') {
    const authHeader = req.headers['authorization'] || '';
    if (authHeader.startsWith('Bearer ')) {
      authSessions.delete(authHeader.substring(7).trim());
    }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true }));
    return;
  }

  // ================= 鉴权拦截器 =================
  const isPublicApi = ['/api/status', '/api/auth/status', '/api/auth/login', '/api/auth/setup', '/api/prefer/ping', '/api/ping-all', '/api/memory/status'].includes(pathname) ||
                      (pathname.startsWith('/api/cf-panel/') && pathname !== '/api/cf-panel/target');

  if (pathname.startsWith('/api/') && !isPublicApi && !checkAuth(req)) {
    res.writeHead(401, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: false, error: '请先登录以执行管理员操作' }));
    return;
  }

  // 1. 系统指标 API
  if (pathname === '/api/status' && req.method === 'GET') {
    const healthObj = {};
    const now = Date.now();
    for (const [k, v] of backendHealth.entries()) {
      if (!v.isHealthy && now > v.blockUntil) {
        backendHealth.delete(k);
      } else {
        healthObj[k] = v;
      }
    }
    const accountsBrief = (cachedCfAccounts?.accounts || []).map(a => ({
      id: a.id,
      name: a.name,
      accountId: a.accountId,
      workers: a.workers || 0,
      pages: a.pages || 0,
      total: a.total || 0,
      max: a.max || 100000,
      percent: a.percent || 0
    }));

    const statsObj = {};
    for (const [k, v] of backendStats.entries()) {
      const total = v.total || 0;
      const success = v.success || 0;
      const rate = total > 0 ? ((success / total) * 100).toFixed(1) : '100.0';
      statsObj[k] = { ...v, successRate: parseFloat(rate) };
    }

    const memStats = getMemoryStats();
    const data = {
      uptime: Math.floor((Date.now() - metrics.startTime) / 1000),
      memoryMB: memStats.rssMB,
      memoryDetail: memStats,
      metrics,
      health: healthObj,
      preferNodes: getPreferNodesList(),
      trafficHistory,
      backendStats: statsObj,
      disabledBackends: currentConfig.disabledBackends || [],
      config: {
        workerBackends: currentConfig.workerBackends,
        pagesBackends: currentConfig.pagesBackends,
        defaultPrefer: currentConfig.defaultPrefer,
        backendAccountMap: currentConfig.backendAccountMap || {},
        quotaDisplayMode: currentConfig.quotaDisplayMode || 'protocol',
        disabledBackends: currentConfig.disabledBackends || [],
        cfQuotaAvoidThreshold: currentConfig.cfQuotaAvoidThreshold !== undefined ? currentConfig.cfQuotaAvoidThreshold : 95,
        memoryGuard: currentConfig.memoryGuard || { enabled: true, thresholdMB: 150, intervalMinutes: 5 }
      },
      cfAccountsBrief: accountsBrief,
      cfQuotaLastUpdated: lastCfAccountsFetch ? new Date(lastCfAccountsFetch).toLocaleTimeString('zh-CN', { hour12: false }) : null,
      cfQuotaIntervalMinutes: Math.max(1, parseInt(currentConfig.cfQuotaIntervalMinutes || '5', 10))
    };
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(data));
    return;
  }

  // 2. 优选节点管理 API
  if (pathname === '/api/prefer/nodes' && req.method === 'GET') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true, nodes: getPreferNodesList() }));
    return;
  }

  if ((pathname === '/api/prefer/ping' || pathname === '/api/ping-all') && (req.method === 'GET' || req.method === 'POST')) {
    const targetQuery = url.searchParams.get('target');
    let targetsToPing = targetQuery ? [targetQuery] : getPreferNodesList().map(n => n.target);
    if (targetsToPing.length === 0 && currentConfig.defaultPrefer) {
      targetsToPing = [currentConfig.defaultPrefer];
    }
    const limitedTargets = targetsToPing.slice(0, 25);
    const results = await Promise.all(limitedTargets.map(t => pingPreferTarget(t, 3500)));

    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({
      ok: true,
      count: results.length,
      results,
      nodes: getPreferNodesList()
    }));
    return;
  }

  if (pathname === '/api/prefer/set-default' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const { target } = JSON.parse(body);
        if (!target || typeof target !== 'string') {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '目标不能为空' }));
          return;
        }
        const trimmed = target.trim();
        currentConfig.defaultPrefer = trimmed;
        saveConfigToFile(currentConfig);
        recordPreferActivity(trimmed);
        console.log(`[Prefer Manager] 默认优选目标已由控制台更新为: ${trimmed}`);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, defaultPrefer: trimmed, nodes: getPreferNodesList() }));
      } catch (err) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: err.message }));
      }
    });
    return;
  }

  if (pathname === '/api/prefer/clear' && req.method === 'POST') {
    const defaultNorm = normalizePreferTarget(currentConfig.defaultPrefer);
    for (const [k, v] of preferNodes.entries()) {
      if (!v.isDefault && k !== defaultNorm.target) {
        preferNodes.delete(k);
      }
    }
    console.log('[Prefer Manager] 已清理非默认历史优选节点记录');
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true, nodes: getPreferNodesList() }));
    return;
  }

  // 3. 单节点启停维护
  if (pathname === '/api/backend/toggle' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const { backend, disabled } = JSON.parse(body);
        if (!backend || typeof backend !== 'string') {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: '缺少 backend 目标节点' }));
          return;
        }
        const b = backend.trim();
        let disabledList = currentConfig.disabledBackends || [];
        if (disabled) {
          if (!disabledList.includes(b)) disabledList.push(b);
        } else {
          disabledList = disabledList.filter(item => item !== b);
        }
        currentConfig.disabledBackends = disabledList;
        saveConfigToFile(currentConfig);
        console.log(`[Backend Switch] 节点 ${b} 状态已被设为: ${disabled ? '手动暂停 (移出池)' : '正常启用'}`);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, backend: b, disabled, disabledBackends: disabledList }));
      } catch (err) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: err.message }));
      }
    });
    return;
  }

  // 4. 配置读写与历史快照
  if (pathname === '/api/config' && req.method === 'GET') {
    const safeCfg = { ...currentConfig };
    if (safeCfg.auth) {
      safeCfg.auth = {
        enabled: !!safeCfg.auth.passwordHash,
        username: safeCfg.auth.username || 'admin'
      };
    }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(safeCfg));
    return;
  }

  if (pathname === '/api/config/backups' && req.method === 'GET') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true, backups: getBackupFilesList() }));
    return;
  }

  if (pathname === '/api/config/rollback' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const { id } = JSON.parse(body);
        const restored = rollbackConfigFile(id);
        console.log(`[Config Backup] 已成功回滚至快照: ${id}`);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, config: restored }));
      } catch (err) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: err.message }));
      }
    });
    return;
  }

  if (pathname === '/api/config' && req.method === 'POST') {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      try {
        const update = JSON.parse(body);
        if (update.defaultPrefer) currentConfig.defaultPrefer = update.defaultPrefer;
        if (Array.isArray(update.workerBackends)) currentConfig.workerBackends = update.workerBackends;
        if (Array.isArray(update.pagesBackends)) currentConfig.pagesBackends = update.pagesBackends;
        if (update.trafficSampleMinutes !== undefined) {
          const m = parseInt(update.trafficSampleMinutes, 10);
          if (!isNaN(m) && m >= 1) {
            currentConfig.trafficSampleMinutes = m;
            startTrafficSampler(metrics);
          }
        }
        if (update.cfQuotaIntervalMinutes !== undefined) {
          const qm = parseInt(update.cfQuotaIntervalMinutes, 10);
          if (!isNaN(qm) && qm >= 1) {
            currentConfig.cfQuotaIntervalMinutes = qm;
          }
        }
        if (update.backendAccountMap !== undefined && typeof update.backendAccountMap === 'object') {
          currentConfig.backendAccountMap = update.backendAccountMap;
        }
        if (update.cfQuotaAvoidThreshold !== undefined) {
          const th = parseInt(update.cfQuotaAvoidThreshold, 10);
          if (!isNaN(th) && th >= 1 && th <= 100) {
            currentConfig.cfQuotaAvoidThreshold = th;
          }
        }
        if (update.quotaDisplayMode !== undefined) {
          currentConfig.quotaDisplayMode = update.quotaDisplayMode === 'total' ? 'total' : 'protocol';
        }
        if (update.loadBalanceMode !== undefined) {
          currentConfig.loadBalanceMode = update.loadBalanceMode === 'round-robin' ? 'round-robin' : 'sticky';
        }
        if (update.stickyDurationSec !== undefined) {
          const s = parseInt(update.stickyDurationSec, 10);
          if (!isNaN(s) && s >= 5 && s <= 3600) {
            currentConfig.stickyDurationSec = s;
          }
        }
        if (update.memoryGuard) {
          currentConfig.memoryGuard = {
            ...currentConfig.memoryGuard,
            ...update.memoryGuard
          };
          startMemoryGuard();
        }
        if (update.telegram) {
          if (update.telegram.chatId) update.telegram.chatId = cleanTelegramChatId(update.telegram.chatId);
          currentConfig.telegram = { ...currentConfig.telegram, ...update.telegram };
        }
        const saved = saveConfigToFile(currentConfig);
        startQuotaPatrol();
        console.log(`[Web Console] 配置已由管理面板更新并热应用！(持久化保存: ${saved ? '成功' : '内存更新'})`);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, saved }));
      } catch (err) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: err.message }));
      }
    });
    return;
  }

  // 5. 实时日志 SSE API
  if (pathname === '/api/logs/stream' && req.method === 'GET') {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache, no-transform',
      'Connection': 'keep-alive',
      'X-Accel-Buffering': 'no'
    });
    res.write(`data: ${JSON.stringify({ type: 'init', logs: logBuffer })}\n\n`);

    const onLog = (item) => {
      try { res.write(`data: ${JSON.stringify({ type: 'log', log: item })}\n\n`); } catch {}
    };

    logEmitter.on('log', onLog);
    const keepAlive = setInterval(() => {
      try { res.write(': keepalive\n\n'); } catch {}
    }, 15000);

    const cleanup = () => {
      logEmitter.removeListener('log', onLog);
      clearInterval(keepAlive);
    };
    req.on('close', cleanup);
    res.on('close', cleanup);
    res.on('finish', cleanup);
    res.on('error', cleanup);
    return;
  }

  // 6. 内存状态与深度垃圾回收 API
  if (pathname === '/api/memory/status' && req.method === 'GET') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true, data: getMemoryStats(), config: currentConfig.memoryGuard }));
    return;
  }

  if (pathname === '/api/memory/gc' && req.method === 'POST') {
    const result = performGarbageCollection('控制台手动一键回收');
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(result));
    return;
  }

  // 7. 重置熔断状态 API
  if (pathname === '/api/circuit/reset' && req.method === 'POST') {
    resetAllCircuit();
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true, message: '所有后端熔断状态已重置' }));
    return;
  }

  // 7. 测试 Telegram 连通性
  if (pathname === '/api/telegram/test' && req.method === 'POST') {
    const timeStr = new Date().toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' });
    const payload = JSON.stringify({
      chat_id: cleanTelegramChatId(currentConfig.telegram.chatId),
      text: `🔔 *【EdgeTunnel 本地分流网关测试通知】*\n\n✅ 恭喜！网关与 Telegram Bot 通道测试握手成功。\n⏰ 发送时间: ${timeStr}`,
      parse_mode: 'Markdown'
    });

    sendTelegramRequest('sendMessage', payload, (rawBody) => {
      try {
        const parsed = JSON.parse(rawBody);
        if (parsed.ok) {
          console.log('[Telegram Bot] ✅ 测试推送消息发送成功');
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: true }));
        } else {
          console.error(`[Telegram Bot] ❌ Telegram API 拒绝: ${parsed.description || rawBody}`);
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: `Telegram 拒绝: ${parsed.description || rawBody}` }));
        }
      } catch (e) {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, raw: rawBody }));
      }
    }, (err) => {
      console.error(`[Telegram Bot] ❌ 测试消息发送失败: ${err.message}`);
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: false, error: err.message }));
    });
    return;
  }

  // 8. 重启服务 API
  if (pathname === '/api/restart' && req.method === 'POST') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true, message: '正在安全重启网关...' }));
    console.log('[Web Console] 收到重启指令，进程将在 500ms 后安全退出并由 Docker 重新拉起...');
    setTimeout(() => process.exit(0), 500);
    return;
  }

  res.writeHead(404, { 'Content-Type': 'text/plain' });
  res.end('Not Found');
});

// ==========================================
// 4. 服务协同启动入口
// ==========================================
const wsServer = createWsServer(metrics, () => cachedCfAccounts);
const xhttpGateway = createXhttpGateway(metrics, () => cachedCfAccounts);

wsServer.listen(currentConfig.httpPort, '0.0.0.0', () => {
  xhttpGateway.listen(currentConfig.h2cPort, '0.0.0.0', () => {
    webServer.listen(currentConfig.webPort, '0.0.0.0', () => {
      console.log('='.repeat(60));
      console.log(`🚀 EdgeTunnel 本地自适应分流网关已全部启动！(模块化架构版)`);
      console.log(`   🔌 WebSocket (HTTP/1.1) 监听端口: ${currentConfig.httpPort}`);
      console.log(`      • Pages 后端池: ${currentConfig.pagesBackends.join(', ')}`);
      console.log(`   ⚡ XHTTP 双模监听端口:    ${currentConfig.h2cPort} (兼容 Mihomo H2 与 v2rayN H1)`);
      console.log(`      • Worker 后端池: ${currentConfig.workerBackends.join(', ')}`);
      console.log(`   🌐 默认实际 Server:     ${currentConfig.defaultPrefer}`);
      console.log(`   🖥️ Web UI 管理控制台:    http://0.0.0.0:${currentConfig.webPort}`);
      startQuotaPatrol();
      recordPreferActivity(currentConfig.defaultPrefer, '系统启动初始化');
      setTimeout(() => {
        getCfSubAccounts(false).catch(() => {});
      }, 1500);
      const tgModeStr = currentConfig.telegram.proxy ? `已启用 (SOCKS5/代理: ${currentConfig.telegram.proxy})` : '已启用 (直连模式)';
      console.log(`   📢 Telegram 告警通知:   ${currentConfig.telegram.botToken ? tgModeStr : '未配置 (已跳过)'}`);
      const mgModeStr = (currentConfig.memoryGuard?.enabled !== false)
        ? `已启用 (阈值: ${currentConfig.memoryGuard?.thresholdMB || 150}MB, 巡检: 每 ${currentConfig.memoryGuard?.intervalMinutes || 5} 分钟, 原生GC: ${typeof global.gc === 'function' ? '已就绪' : '未开启--expose-gc'})`
        : '未启用';
      console.log(`   🧠 内存守护与自动回收: ${mgModeStr}`);
      console.log('='.repeat(60) + '\n');

      verifyTelegramConnectivity();
    });
  });
});

process.on('uncaughtException', (err) => {
  if (isBenignError(err)) return;
  console.error('[UncaughtException Warning]', err.message);
});
