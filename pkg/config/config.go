package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"edgetunnel-lb/pkg/dialer"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
)

type Manager struct {
	mu         sync.RWMutex
	cfg        model.Config
	configPath string
	backupDir  string
}

var globalManager *Manager

func InitConfig(configPath string) (*Manager, error) {
	if configPath == "" {
		configPath = "config.json"
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		absPath = configPath
	}

	backupDir := filepath.Join(filepath.Dir(absPath), "backups")
	_ = os.MkdirAll(backupDir, 0755)

	m := &Manager{
		configPath: absPath,
		backupDir:  backupDir,
	}

	if err := m.load(); err != nil {
		logger.Warnf("加载配置文件失败 (%s)，使用缺省配置: %v", absPath, err)
		m.applyDefaults()
	}

	globalManager = m
	return m, nil
}

func GetManager() *Manager {
	return globalManager
}

func (m *Manager) Get() model.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) applyDefaults() {
	if m.cfg.HTTPPort <= 0 {
		m.cfg.HTTPPort = 8787
	}
	if m.cfg.H2CPort <= 0 {
		m.cfg.H2CPort = 8788
	}
	if m.cfg.WebPort <= 0 {
		m.cfg.WebPort = 8789
	}
	if m.cfg.DefaultPrefer == "" {
		m.cfg.DefaultPrefer = "cf.090227.xyz"
	}
	if m.cfg.TrafficSampleMinutes <= 0 {
		m.cfg.TrafficSampleMinutes = 5
	}
	if m.cfg.CFQuotaIntervalMinutes <= 0 {
		m.cfg.CFQuotaIntervalMinutes = 5
	}
	if m.cfg.CFQuotaAvoidThreshold <= 0 {
		m.cfg.CFQuotaAvoidThreshold = 95
	}
	if m.cfg.CFAlertThreshold <= 0 {
		m.cfg.CFAlertThreshold = 80
	}
	if m.cfg.CFQuotaMode == "" {
		m.cfg.CFQuotaMode = "usagepanel"
	}
	if m.cfg.QuotaDisplayMode == "" {
		m.cfg.QuotaDisplayMode = "protocol"
	}
	if m.cfg.BackendAccountMap == nil {
		m.cfg.BackendAccountMap = make(map[string]string)
	}
	if m.cfg.DisabledBackends == nil {
		m.cfg.DisabledBackends = make([]string, 0)
	}
	if m.cfg.MemoryGuard.ThresholdMB <= 0 {
		m.cfg.MemoryGuard.ThresholdMB = 150
	}
	if m.cfg.MemoryGuard.IntervalMinutes <= 0 {
		m.cfg.MemoryGuard.IntervalMinutes = 5
	}
	if len(m.cfg.DNS) == 0 {
		m.cfg.DNS = []string{"223.5.5.5", "119.29.29.29"}
	}
	dialer.SetDNS(m.cfg.DNS)
}

func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return err
	}

	var cfg model.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	m.cfg = cfg
	m.applyDefaults()
	logger.Infof("已成功加载配置文件: %s (Workers: %d, Pages: %d)", m.configPath, len(m.cfg.WorkerBackends), len(m.cfg.PagesBackends))
	return nil
}

// writeFileAtomic 原子写入文件：先写入同目录临时文件并刷盘，再通过 os.Rename 原子覆盖，防止断电或闪存满导致配置文件损坏变空
func writeFileAtomic(targetPath string, data []byte, perm os.FileMode) error {
	tmpPath := targetPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, targetPath)
}

func (m *Manager) Save(newCfg model.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 创建备份
	m.createBackupUnlocked()

	// 2. 原子写入新配置
	data, err := json.MarshalIndent(newCfg, "", "  ")
	if err != nil {
		return err
	}

	if err := writeFileAtomic(m.configPath, data, 0644); err != nil {
		return err
	}

	m.cfg = newCfg
	m.applyDefaults()
	logger.Infof("配置已持久化保存并热应用: %s", m.configPath)
	return nil
}

func (m *Manager) Update(fn func(cfg *model.Config)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createBackupUnlocked()

	fn(&m.cfg)
	m.applyDefaults()

	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(m.configPath, data, 0644)
}

func (m *Manager) createBackupUnlocked() {
	if _, err := os.Stat(m.configPath); err != nil {
		return
	}
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return
	}

	timestamp := time.Now().Format("20060102-150405")
	backupFile := filepath.Join(m.backupDir, fmt.Sprintf("config-%s.json", timestamp))
	_ = os.WriteFile(backupFile, data, 0644)

	// 精简保留最新 5 份快照备份，节省软路由受限闪存空间
	const maxBackups = 5
	entries, err := os.ReadDir(m.backupDir)
	if err == nil && len(entries) > maxBackups {
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "config-") && strings.HasSuffix(e.Name(), ".json") {
				files = append(files, filepath.Join(m.backupDir, e.Name()))
			}
		}
		sort.Strings(files)
		for i := 0; i < len(files)-maxBackups; i++ {
			_ = os.Remove(files[i])
		}
	}
}

type BackupItem struct {
	ID       string `json:"id"`
	FileName string `json:"fileName"`
	Time     string `json:"time"`
	Size     int64  `json:"size"`
}

func (m *Manager) GetBackupsList() []BackupItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries, err := os.ReadDir(m.backupDir)
	if err != nil {
		return nil
	}

	var list []BackupItem
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "config-") && strings.HasSuffix(e.Name(), ".json") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "config-"), ".json")
			list = append(list, BackupItem{
				ID:       id,
				FileName: e.Name(),
				Time:     info.ModTime().Format("2006-01-02 15:04:05"),
				Size:     info.Size(),
			})
		}
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].ID > list[j].ID
	})
	return list
}

func (m *Manager) Rollback(id string) (model.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	targetFile := filepath.Join(m.backupDir, fmt.Sprintf("config-%s.json", id))
	data, err := os.ReadFile(targetFile)
	if err != nil {
		return m.cfg, fmt.Errorf("找不到指定的快照文件: %v", err)
	}

	var restoredCfg model.Config
	if err := json.Unmarshal(data, &restoredCfg); err != nil {
		return m.cfg, fmt.Errorf("快照解析失败: %v", err)
	}

	if err := os.WriteFile(m.configPath, data, 0644); err != nil {
		return m.cfg, fmt.Errorf("写入恢复配置失败: %v", err)
	}

	m.cfg = restoredCfg
	m.applyDefaults()
	logger.Infof("已成功将配置回滚至快照: %s", id)
	return m.cfg, nil
}

func CleanTelegramChatID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "g_")
	return id
}
