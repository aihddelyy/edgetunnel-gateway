import http from 'node:http';
import https from 'node:https';
import { currentConfig } from './config.mjs';
import { getNextPagesBackend, markBackendSuccess, markBackendFailure } from './health.mjs';
import { parseTargetHostPort, recordPreferActivity } from './prefer.mjs';
import { parsePreferAndCleanPath, isBenignError } from './utils.mjs';

export function createWsServer(metrics, getCachedCfAccounts = () => null) {
  const wsServer = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/plain; charset=utf-8' });
    res.end('EdgeTunnel WS Gateway is running.\n');
  });

  wsServer.on('upgrade', (req, clientSocket, head) => {
    if (metrics) metrics.wsRequests++;
    if (typeof clientSocket.setNoDelay === 'function') {
      clientSocket.setNoDelay(true);
    }
    if (typeof clientSocket.setKeepAlive === 'function') {
      clientSocket.setKeepAlive(true, 15000);
    }
    const clientIp = req.socket.remoteAddress || '';
    const clientTag = `${clientIp || '未知'}:${req.socket.remotePort || ''}`;
    const rawPath = req.url || '/';
    const { targetServer: extractedServer, cleanedPath } = parsePreferAndCleanPath(rawPath);
    const targetServer = extractedServer || currentConfig.defaultPrefer;
    const { host: connectHost, port: connectPort } = parseTargetHostPort(targetServer);

    recordPreferActivity(targetServer, req.socket.remoteAddress);

    const outHeadersBase = {};
    for (const [key, value] of Object.entries(req.headers)) {
      if (!['host', 'upgrade', 'connection'].includes(key.toLowerCase())) {
        outHeadersBase[key] = value;
      }
    }
    outHeadersBase['upgrade'] = 'websocket';
    outHeadersBase['connection'] = 'Upgrade';

    const maxRetries = 2;
    const triedBackends = [];

    function attemptForwardWs(attempt = 0) {
      if (clientSocket.destroyed || !clientSocket.writable) return;

      const backendSni = getNextPagesBackend(triedBackends, getCachedCfAccounts());
      triedBackends.push(backendSni);
      const remoteHostHeader = backendSni;

      const outHeaders = { ...outHeadersBase, host: remoteHostHeader };

      if (attempt === 0) {
        console.log(`------------------------------------------------------------\n` +
                    `[WS/Upgrade #${clientTag}] 收到握手请求:\n` +
                    `  📥 [客户端入站 Ingress]\n` +
                    `     • 客户端来源: ${clientTag}\n` +
                    `     • 客户端 Host: ${req.headers.host || '未设置'}\n` +
                    `     • 客户端原始请求路径: ${rawPath}\n` +
                    `  📤 [网关转发出站 Egress]\n` +
                    `     • 实际 Server: ${connectHost}:${connectPort}\n` +
                    `     • 实际 TLS SNI: ${backendSni} (Pages后端)\n` +
                    `     • 实际 HTTP Host: ${remoteHostHeader}\n` +
                    `     • 实际请求路径: ${cleanedPath}\n` +
                    `------------------------------------------------------------`);
      } else {
        console.warn(`[WS Failover #${clientTag}] 🔄 正在透明故障转移至备用 Pages 后端 (第 ${attempt} 次重试): ${backendSni}`);
      }

      const startTime = Date.now();
      let isHandshakeDone = false;

      const proxyReq = https.request({
        host: connectHost,
        port: connectPort,
        servername: backendSni,
        path: cleanedPath,
        method: 'GET',
        headers: outHeaders,
        timeout: 7000,
        rejectUnauthorized: false
      });

      const onClientError = (err) => {
        if (!isBenignError(err)) console.error(`[WS 客户端连接断开 #${clientTag}]:`, err.message);
        try { proxyReq.destroy(); } catch {}
      };
      clientSocket.once('error', onClientError);

      proxyReq.on('upgrade', (proxyRes, proxySocket, proxyHead) => {
        isHandshakeDone = true;
        clientSocket.removeListener('error', onClientError);
        if (typeof proxySocket.setNoDelay === 'function') {
          proxySocket.setNoDelay(true);
        }
        if (typeof proxySocket.setKeepAlive === 'function') {
          proxySocket.setKeepAlive(true, 15000);
        }
        const elapsed = Date.now() - startTime;
        markBackendSuccess(backendSni, elapsed);
        recordPreferActivity(targetServer, req.socket.remoteAddress, elapsed, 'ok');
        console.log(`[WS/Upgrade #${clientTag}] ✅ 握手成功: 101 Switching Protocols (耗时: ${elapsed}ms) | 实际 Server: ${connectHost}:${connectPort} | SNI: ${backendSni}`);
        
        let respRaw = `HTTP/1.1 101 Switching Protocols\r\n`;
        for (const [key, value] of Object.entries(proxyRes.headers)) {
          if (Array.isArray(value)) {
            for (const v of value) respRaw += `${key}: ${v}\r\n`;
          } else {
            respRaw += `${key}: ${value}\r\n`;
          }
        }
        respRaw += `\r\n`;

        // 一次性合并发送 101 升级响应与初始头碎片，避免 TCP 分包
        const respBuf = Buffer.from(respRaw, 'utf8');
        if (proxyHead && proxyHead.length > 0) {
          clientSocket.write(Buffer.concat([respBuf, proxyHead]));
        } else {
          clientSocket.write(respBuf);
        }

        if (head && head.length > 0) {
          proxySocket.write(head);
        }

        proxySocket.pipe(clientSocket);
        clientSocket.pipe(proxySocket);

        const cleanupSockets = () => {
          try { proxySocket.destroy(); } catch {}
          try { clientSocket.destroy(); } catch {}
        };

        proxySocket.on('close', cleanupSockets);
        clientSocket.on('close', cleanupSockets);
        proxySocket.on('error', (err) => {
          if (!isBenignError(err)) console.error(`[WS/Egress Error #${clientTag}]:`, err.message);
          cleanupSockets();
        });
        clientSocket.on('error', (err) => {
          if (!isBenignError(err)) console.error(`[WS/Ingress Error #${clientTag}]:`, err.message);
          cleanupSockets();
        });
      });

      proxyReq.on('response', (res) => {
        let body = '';
        res.on('data', c => { if (body.length < 500) body += c.toString('utf8'); });
        res.on('end', () => {
          const errReason = `HTTP状态码 ${res.statusCode} (响应体: ${body.trim().substring(0, 100)})`;
          markBackendFailure(backendSni, errReason, metrics);
          if (res.statusCode >= 400) {
            recordPreferActivity(targetServer, req.socket.remoteAddress, null, 'warn', `HTTP ${res.statusCode}`);
          }
        });

        const canRetry = attempt < maxRetries && (res.statusCode === 1101 || res.statusCode === 502 || res.statusCode === 503 || res.statusCode === 429 || res.statusCode === 403 || res.statusCode === 521 || res.statusCode === 522);
        if (canRetry && clientSocket.writable && !clientSocket.destroyed) {
          console.warn(`[WS Failover #${clientTag}] ⚠️ 后端 ${backendSni} 响应异常码 ${res.statusCode}，透明切换备用节点...`);
          clientSocket.removeListener('error', onClientError);
          try { proxyReq.destroy(); } catch {}
          attemptForwardWs(attempt + 1);
          return;
        }

        clientSocket.write(`HTTP/1.1 ${res.statusCode} ${res.statusMessage}\r\nConnection: close\r\n\r\n`);
        clientSocket.end();
      });

      proxyReq.on('timeout', () => {
        proxyReq.destroy(new Error('优选握手超时(>7s)'));
      });

      proxyReq.on('error', (err) => {
        if (isHandshakeDone) return;
        if (!isBenignError(err)) {
          console.warn(`[WS 优选链路连接异常 #${clientTag} -> ${connectHost}:${connectPort}]: ${err.message} (交由客户端策略组自动切流)`);
          recordPreferActivity(targetServer, req.socket.remoteAddress, null, 'error', err.message);
        }
        clientSocket.destroy();
      });

      proxyReq.end();
    }

    attemptForwardWs(0);
  });

  return wsServer;
}
