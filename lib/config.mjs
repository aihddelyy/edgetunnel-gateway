import fs from 'node:fs';
import path from 'node:path';

export const CONFIG_FILE = path.resolve(process.cwd(), 'config.json');
export const BACKUPS_DIR = path.resolve(process.cwd(), 'config_backups');
export const TRAFFIC_HISTORY_FILE = path.resolve(process.cwd(), 'traffic_history.json');
export const PUBLIC_HTML_PATH = path.resolve(process.cwd(), 'public', 'index.html');

export function cleanTelegramChatId(id) {
  if (!id) return '';
  return String(id).trim().replace(/^['"]|['"]$/g, '').trim();
}

export const DEFAULT_CONFIG = {
  httpPort: parseInt(process.env.PORT || '8787', 10),
  h2cPort: parseInt(process.env.H2C_PORT || '8788', 10),
  webPort: parseInt(process.env.WEB_PORT || '8789', 10),
  defaultPrefer: process.env.PREFERRED_SERVER || process.env.DEFAULT_PREFER || 'cf.example.com',
  trafficSampleMinutes: parseInt(process.env.TRAFFIC_SAMPLE_MINUTES || '5', 10),
  cfPanelUrl: (process.env.CF_PANEL_URL || 'https://usagepanel.pages.dev').trim(),
  cfQuotaMode: 'usagepanel',
  cfAlertThreshold: 80,
  cfQuotaIntervalMinutes: 5,
  cfQuotaAvoidThreshold: 95,
  cfDirect: { accounts: [] },
  cfPanel: {
    url: (process.env.CF_PANEL_URL || 'https://usagepanel.pages.dev').trim(),
    username: (process.env.CF_PANEL_USER || '').trim(),
    password: (process.env.CF_PANEL_PASS || '').trim(),
    alertThreshold: parseInt(process.env.CF_PANEL_ALERT_THRESHOLD || '80', 10)
  },
  pagesBackends: (process.env.PAGES_BACKENDS || process.env.BACKENDS || '')
    .split(',').map(s => s.trim()).filter(Boolean),
  workerBackends: (process.env.WORKER_BACKENDS || process.env.BACKENDS || '')
    .split(',').map(s => s.trim()).filter(Boolean),
  disabledBackends: [],
  backendAccountMap: {},
  quotaDisplayMode: 'protocol',
  loadBalanceMode: process.env.LOAD_BALANCE_MODE || 'round-robin',
  stickyDurationSec: parseInt(process.env.STICKY_DURATION_SEC || '60', 10),
  auth: {
    enabled: false,
    username: 'admin',
    passwordHash: '',
    salt: ''
  },
  telegram: {
    botToken: (process.env.TG_BOT_TOKEN || process.env.TG_TOKEN || process.env.TELEGRAM_BOT_TOKEN || '').trim(),
    chatId: cleanTelegramChatId(process.env.TG_CHAT_ID || process.env.TG_CHATID || process.env.TELEGRAM_CHAT_ID || ''),
    proxy: (process.env.TG_PROXY || process.env.TELEGRAM_PROXY || '').trim(),
    apiHost: (process.env.TG_API_HOST || 'api.telegram.org').trim()
  },
  memoryGuard: {
    enabled: true,
    thresholdMB: parseInt(process.env.MEMORY_THRESHOLD_MB || '150', 10),
    intervalMinutes: parseInt(process.env.MEMORY_INTERVAL_MINUTES || '5', 10)
  }
};

export let currentConfig = { ...DEFAULT_CONFIG };

export function loadConfigFromFile() {
  try {
    if (fs.existsSync(CONFIG_FILE)) {
      const raw = fs.readFileSync(CONFIG_FILE, 'utf8');
      const parsed = JSON.parse(raw);
      currentConfig = {
        ...DEFAULT_CONFIG,
        ...parsed,
        backendAccountMap: parsed.backendAccountMap || {},
        quotaDisplayMode: parsed.quotaDisplayMode || 'protocol',
        telegram: { ...DEFAULT_CONFIG.telegram, ...(parsed.telegram || {}) },
        auth: { ...DEFAULT_CONFIG.auth, ...(parsed.auth || {}) },
        memoryGuard: { ...DEFAULT_CONFIG.memoryGuard, ...(parsed.memoryGuard || {}) }
      };
      console.log(`[Config] 已成功加载本地持久化配置文件: ${CONFIG_FILE}`);
      return currentConfig;
    }
  } catch (e) {
    console.warn(`[Config] 读取本地 config.json 失败，将回退至环境变量: ${e.message}`);
  }
  return currentConfig;
}

export function saveConfigToFile(newCfg, skipBackup = false) {
  try {
    // 自动备份快照机制 (保留最近 5 份快照)
    if (!skipBackup && fs.existsSync(CONFIG_FILE)) {
      try {
        if (!fs.existsSync(BACKUPS_DIR)) fs.mkdirSync(BACKUPS_DIR, { recursive: true });
        const oldContent = fs.readFileSync(CONFIG_FILE, 'utf8');
        const backupFile = path.resolve(BACKUPS_DIR, `config_backup_${Date.now()}.json`);
        fs.writeFileSync(backupFile, oldContent, 'utf8');

        // 清理超出 5 份的历史备份
        const files = fs.readdirSync(BACKUPS_DIR)
          .filter(f => f.startsWith('config_backup_') && f.endsWith('.json'))
          .sort((a, b) => b.localeCompare(a));
        if (files.length > 5) {
          for (let i = 5; i < files.length; i++) {
            try { fs.unlinkSync(path.resolve(BACKUPS_DIR, files[i])); } catch {}
          }
        }
      } catch (bErr) {
        console.warn(`[Config Backup] 创建配置备份快照失败: ${bErr.message}`);
      }
    }

    fs.writeFileSync(CONFIG_FILE, JSON.stringify(newCfg, null, 2), 'utf8');
    return true;
  } catch (err) {
    console.warn(`[Config] 保存 config.json 失败 (可能为只读挂载): ${err.message}`);
    return false;
  }
}

export function getBackupFilesList() {
  if (!fs.existsSync(BACKUPS_DIR)) return [];
  const files = fs.readdirSync(BACKUPS_DIR)
    .filter(f => f.startsWith('config_backup_') && f.endsWith('.json'))
    .sort((a, b) => b.localeCompare(a));
  
  return files.map(file => {
    try {
      const content = JSON.parse(fs.readFileSync(path.resolve(BACKUPS_DIR, file), 'utf8'));
      const timestamp = parseInt(file.replace('config_backup_', '').replace('.json', ''), 10);
      const d = new Date(timestamp);
      const pad = n => String(n).padStart(2, '0');
      const timeStr = `${d.getFullYear()}/${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
      return {
        id: file,
        time: timeStr,
        defaultPrefer: content.defaultPrefer || '--',
        workersCount: (content.workerBackends || []).length,
        pagesCount: (content.pagesBackends || []).length,
        sampleMinutes: content.trafficSampleMinutes || 5
      };
    } catch {
      return null;
    }
  }).filter(Boolean);
}

export function rollbackConfigFile(id) {
  if (!id || typeof id !== 'string' || !id.startsWith('config_backup_') || !id.endsWith('.json')) {
    throw new Error('无效的备份 ID');
  }
  const targetPath = path.resolve(BACKUPS_DIR, id);
  if (!fs.existsSync(targetPath)) {
    throw new Error('快照文件不存在');
  }

  const restoredCfg = JSON.parse(fs.readFileSync(targetPath, 'utf8'));
  // 保留当前鉴权信息防失联
  if (currentConfig.auth && !restoredCfg.auth) {
    restoredCfg.auth = currentConfig.auth;
  }
  currentConfig = { ...DEFAULT_CONFIG, ...restoredCfg };
  saveConfigToFile(currentConfig);
  return currentConfig;
}
