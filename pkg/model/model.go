package model

// Config represents the application configuration
type Config struct {
	HTTPPort               int               `json:"httpPort"`
	H2CPort                int               `json:"h2cPort"`
	WebPort                int               `json:"webPort"`
	DefaultPrefer          string            `json:"defaultPrefer"`
	TrafficSampleMinutes   int               `json:"trafficSampleMinutes"`
	CFQuotaIntervalMinutes int               `json:"cfQuotaIntervalMinutes"`
	CFPanelURL             string            `json:"cfPanelUrl"`
	CFQuotaAvoidThreshold  int               `json:"cfQuotaAvoidThreshold"`
	CFAlertThreshold       int               `json:"cfAlertThreshold"`
	CFQuotaMode            string            `json:"cfQuotaMode"` // "usagepanel" or "direct"
	QuotaDisplayMode       string            `json:"quotaDisplayMode"` // "protocol" or "total"
	CFPanel                CFPanelConfig     `json:"cfPanel"`
	CFDirect               CFDirectConfig    `json:"cfDirect"`
	PagesBackends          []string          `json:"pagesBackends"`
	WorkerBackends         []string          `json:"workerBackends"`
	DisabledBackends       []string          `json:"disabledBackends"`
	BackendAccountMap      map[string]string `json:"backendAccountMap"`
	Auth                   AuthConfig        `json:"auth"`
	Telegram               TelegramConfig    `json:"telegram"`
	MemoryGuard            MemoryGuardConfig `json:"memoryGuard"`
}

type CFPanelConfig struct {
	URL            string `json:"url"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	AlertThreshold int    `json:"alertThreshold"`
}

type CFDirectAccount struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AccountID string `json:"accountId"`
	APIToken  string `json:"apiToken"`
}

type CFDirectConfig struct {
	Accounts []CFDirectAccount `json:"accounts"`
}

type AuthConfig struct {
	Enabled      bool   `json:"enabled"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	Salt         string `json:"salt"`
}

type TelegramConfig struct {
	BotToken string `json:"botToken"`
	ChatID   string `json:"chatId"`
	Proxy    string `json:"proxy"`
	APIHost  string `json:"apiHost"`
}

type MemoryGuardConfig struct {
	Enabled         bool `json:"enabled"`
	ThresholdMB     int  `json:"thresholdMB"`
	IntervalMinutes int  `json:"intervalMinutes"`
}

// Metrics tracks runtime gateway statistics
type Metrics struct {
	StartTime     int64 `json:"startTime"`
	WSRequests    int64 `json:"wsRequests"`
	XHTTPRequests int64 `json:"xhttpRequests"`
	H2CRequests   int64 `json:"h2cRequests"`
	H1Requests    int64 `json:"h1Requests"`
	AlertsSent    int64 `json:"alertsSent"`
	CircuitTrips  int64 `json:"circuitTrips"`
}

// BackendStat keeps traffic counts per backend
type BackendStat struct {
	Total       int64   `json:"total"`
	Success     int64   `json:"success"`
	Fail        int64   `json:"fail"`
	SuccessRate float64 `json:"successRate"`
}

// HealthState represents circuit breaker status for a backend
type HealthState struct {
	IsHealthy  bool   `json:"isHealthy"`
	BlockUntil int64  `json:"blockUntil"` // Unix millisecond timestamp
	Reason     string `json:"reason"`
}

// PreferNode records status and metrics of a preferred IP/host
type PreferNode struct {
	Target       string  `json:"target"`
	Host         string  `json:"host"`
	Port         int     `json:"port"`
	IsDefault    bool    `json:"isDefault"`
	ActiveConns  int     `json:"activeConns"`
	SuccessReqs  int     `json:"successReqs"`
	FailReqs     int     `json:"failReqs"`
	LastLatency  int     `json:"lastLatency"` // in ms
	LastStatus   string  `json:"lastStatus"`  // "ok", "warn", "error"
	LastReason   string  `json:"lastReason"`
	LastSeen     int64   `json:"lastSeen"`
	LastSeenTime string  `json:"lastSeenTime"`
}

// CFAccountBrief holds subaccount quota information
type CFAccountBrief struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	AccountID  string                 `json:"accountId"`
	UpdateTime string                 `json:"updateTime"`
	Workers    int64                  `json:"workers"`
	Pages      int64                  `json:"pages"`
	Total      int64                  `json:"total"`
	Max        int64                  `json:"max"`
	Percent    float64                `json:"percent"`
	Error      string                 `json:"error,omitempty"`
	Resources  map[string]interface{} `json:"resources,omitempty"`
}

// CFAccountsResult is the cached quota query response
type CFAccountsResult struct {
	OK       bool             `json:"ok"`
	Mode     string           `json:"mode"`
	Count    int              `json:"count"`
	Accounts []CFAccountBrief `json:"accounts"`
	Error    string           `json:"error,omitempty"`
	Message  string           `json:"message,omitempty"`
	NeedAuth bool             `json:"needAuth,omitempty"`
}

// TrafficPoint records sampled traffic over time
type TrafficPoint struct {
	Time     string `json:"time"`
	WS       int64  `json:"ws"`
	XHTTP    int64  `json:"xhttp"`
	H2C      int64  `json:"h2c"`
	H1       int64  `json:"h1"`
	TotalReq int64  `json:"totalReq"`
}

// LogItem represents an SSE / log message
type LogItem struct {
	ID      int64  `json:"id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}
