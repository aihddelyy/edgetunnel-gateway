package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
	"edgetunnel-lb/pkg/notify"
)

type Manager struct {
	mu            sync.RWMutex
	cachedResult  model.CFAccountsResult
	lastFetchTime time.Time
	alertedToday  map[string]struct{}

	cfgMgr *config.Manager
	cancel context.CancelFunc
}

var globalManager *Manager

func InitQuotaManager(cfgMgr *config.Manager) *Manager {
	m := &Manager{
		alertedToday: make(map[string]struct{}),
		cfgMgr:       cfgMgr,
	}
	globalManager = m

	m.StartPatrol()
	return m
}

func GetManager() *Manager {
	return globalManager
}

func (m *Manager) StartPatrol() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	cfg := m.cfgMgr.Get()
	intervalMinutes := cfg.CFQuotaIntervalMinutes
	if intervalMinutes <= 0 {
		intervalMinutes = 5
	}

	go func() {
		// Initial fetch immediately
		_ = m.FetchAccounts(true)

		ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.FetchAccounts(true)
			}
		}
	}()
}

func (m *Manager) GetCachedAccounts() []model.CFAccountBrief {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cachedResult.Accounts
}

func (m *Manager) GetLastUpdatedStr() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.lastFetchTime.IsZero() {
		return ""
	}
	return m.lastFetchTime.Format("15:04:05")
}

func (m *Manager) FetchAccounts(force bool) model.CFAccountsResult {
	cfg := m.cfgMgr.Get()

	m.mu.RLock()
	if !force && time.Since(m.lastFetchTime) < 60*time.Second && m.cachedResult.OK {
		res := m.cachedResult
		m.mu.RUnlock()
		return res
	}
	m.mu.RUnlock()

	mode := cfg.CFQuotaMode
	if mode == "" {
		if len(cfg.CFDirect.Accounts) > 0 && (cfg.CFPanel.Username == "" || cfg.CFPanel.Password == "") {
			mode = "direct"
		} else {
			mode = "usagepanel"
		}
	}

	var result model.CFAccountsResult
	if mode == "direct" {
		result = m.fetchDirect(cfg)
	} else {
		result = m.fetchUsagePanel(cfg)
	}

	if result.OK {
		m.mu.Lock()
		m.cachedResult = result
		m.lastFetchTime = time.Now()
		m.checkAndTriggerQuotaAlert(result.Accounts, cfg)
		m.mu.Unlock()
	}

	return result
}

// Cloudflare GraphQL query for workers and pages requests
const cfGraphQLQuery = `
query getBillingMetrics($AccountID: String!, $filter: AccountWorkersInvocationsAdaptiveFilter_InputObject) {
  viewer {
    accounts(filter: {accountTag: $AccountID}) {
      pagesFunctionsInvocationsAdaptiveGroups(limit: 1000, filter: $filter) {
        sum {
          requests
        }
      }
      workersInvocationsAdaptive(limit: 10000, filter: $filter) {
        sum {
          requests
        }
      }
    }
  }
}
`

const cfKvGraphQLQuery = `
query KvUsage($accountTag: String!, $dayStart: Date!, $dateEnd: Date!, $storageStart: Date!) {
  viewer {
    accounts(filter: {accountTag: $accountTag}) {
      kvOperationsAdaptiveGroups(limit: 10000, filter: {date_geq: $dayStart, date_leq: $dateEnd}) {
        sum { requests }
        dimensions { actionType }
      }
      kvStorageAdaptiveGroups(limit: 10000, filter: {date_geq: $storageStart, date_leq: $dateEnd}, orderBy: [date_DESC]) {
        max { keyCount byteCount }
        dimensions { date namespaceId }
      }
    }
  }
}
`

const cfD1GraphQLQuery = `
query D1Usage($accountTag: String!, $dayStart: Date!, $dateEnd: Date!, $storageStart: Date!) {
  viewer {
    accounts(filter: {accountTag: $accountTag}) {
      d1AnalyticsAdaptiveGroups(limit: 10000, filter: {date_geq: $dayStart, date_leq: $dateEnd}) {
        sum { rowsRead rowsWritten readQueries writeQueries }
        dimensions { date databaseId }
      }
      d1StorageAdaptiveGroups(limit: 10000, filter: {date_geq: $storageStart, date_leq: $dateEnd}, orderBy: [date_DESC]) {
        max { databaseSizeBytes }
        dimensions { date databaseId }
      }
    }
  }
}
`

const cfR2GraphQLQuery = `
query R2Usage($accountTag: String!, $monthStart: Time!, $now: Time!) {
  viewer {
    accounts(filter: {accountTag: $accountTag}) {
      r2OperationsAdaptiveGroups(limit: 10000, filter: {datetime_geq: $monthStart, datetime_leq: $now}) {
        sum { requests }
        dimensions { actionType actionStatus }
      }
      r2StorageAdaptiveGroups(limit: 10000, filter: {datetime_geq: $monthStart, datetime_leq: $now}, orderBy: [datetime_DESC]) {
        max { objectCount uploadCount payloadSize metadataSize }
        dimensions { datetime bucketName }
      }
    }
  }
}
`

var (
	r2ClassAActions = map[string]bool{
		"listbuckets": true, "putbucket": true, "listobjects": true, "listobjectsv2": true, "putobject": true, "copyobject": true,
		"completemultipartupload": true, "createmultipartupload": true, "lifecyclestoragetiertransition": true,
		"listmultipartuploads": true, "uploadpart": true, "uploadpartcopy": true, "listparts": true,
		"putbucketencryption": true, "putbucketcors": true, "putbucketlifecycleconfiguration": true,
	}
	r2ClassBActions = map[string]bool{
		"headbucket": true, "headobject": true, "getobject": true, "usagesummary": true, "getbucketencryption": true,
		"getbucketlocation": true, "getbucketcors": true, "getbucketlifecycleconfiguration": true,
	}
	r2FreeActions = map[string]bool{
		"deleteobject": true, "deleteobjects": true, "deletebucket": true, "abortmultipartupload": true,
	}
)

func buildDefaultResources() map[string]interface{} {
	return map[string]interface{}{
		"kv": map[string]interface{}{
			"namespaces":        0,
			"reads":             0,
			"readsLimit":        100000,
			"writes":            0,
			"writesLimit":       1000,
			"deletes":           0,
			"lists":             0,
			"deletesLimit":      1000,
			"listsLimit":        1000,
			"operations":        0,
			"storageBytes":      0,
			"storageLimitBytes": 1073741824,
			"keys":              0,
			"period":            "day",
		},
		"d1": map[string]interface{}{
			"databases":         0,
			"rowsRead":          0,
			"rowsReadLimit":     5000000,
			"rowsWritten":       0,
			"rowsWrittenLimit":  100000,
			"readQueries":       0,
			"writeQueries":      0,
			"storageBytes":      0,
			"storageLimitBytes": 5368709120,
			"period":            "day",
		},
		"r2": map[string]interface{}{
			"buckets":           0,
			"classA":            0,
			"classALimit":       1000000,
			"classB":            0,
			"classBLimit":       10000000,
			"storageBytes":      0,
			"storageLimitBytes": 10737418240,
			"operations":        0,
			"objects":           0,
			"period":            "month",
		},
	}
}

func doCFPost(client *http.Client, token string, body interface{}) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "https://api.cloudflare.com/client/v4/graphql", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "EdgeTunnel-LB-CF-Direct/2.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(bodyBytes, &errResp)
		if len(errResp.Errors) > 0 {
			return nil, fmt.Errorf("CF HTTP %d: %s", resp.StatusCode, errResp.Errors[0].Message)
		}
		return nil, fmt.Errorf("CF HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	return bodyBytes, nil
}

func tryCountRestList(client *http.Client, token, url string) int {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "EdgeTunnel-LB-CF-Direct/2.0")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return 0
	}
	defer resp.Body.Close()
	var restResp struct {
		Success bool          `json:"success"`
		Result  []interface{} `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&restResp)
	if restResp.Success {
		return len(restResp.Result)
	}
	return 0
}

func (m *Manager) fetchDirect(cfg model.Config) model.CFAccountsResult {
	accounts := cfg.CFDirect.Accounts
	if len(accounts) == 0 {
		return model.CFAccountsResult{
			OK:       false,
			NeedAuth: true,
			Message:  "尚未配置 Cloudflare 官方 API 直连账户（请在【配置凭据】添加 Account ID 与 API Token）",
		}
	}

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	nowIso := now.Format(time.RFC3339)
	dayStartIso := dayStart.Format(time.RFC3339)
	monthStartIso := monthStart.Format(time.RFC3339)
	dayStartDate := dayStart.Format("2006-01-02")
	monthStartDate := monthStart.Format("2006-01-02")
	dateEnd := now.Format("2006-01-02")

	var briefList []model.CFAccountBrief
	client := &http.Client{Timeout: 15 * time.Second}

	for idx, acc := range accounts {
		accID := strings.TrimSpace(acc.AccountID)
		token := strings.TrimSpace(acc.APIToken)
		name := acc.Name
		if name == "" {
			name = fmt.Sprintf("CF 账户 %d", idx+1)
		}

		if accID == "" || token == "" {
			briefList = append(briefList, model.CFAccountBrief{
				ID:         acc.ID,
				Name:       name,
				AccountID:  accID,
				UpdateTime: time.Now().UTC().Format(time.RFC3339),
				Error:      "未配置 Account ID 或 API Token",
				Resources:  buildDefaultResources(),
			})
			continue
		}

		// 并发执行 4 个 GraphQL 指标拉取
		var (
			wg         sync.WaitGroup
			wpBytes    []byte
			kvBytes    []byte
			d1Bytes    []byte
			r2Bytes    []byte
			wpErr, kvErr, d1Err, r2Err error
		)

		wg.Add(4)
		// 1. WP
		go func() {
			defer wg.Done()
			reqBody := map[string]interface{}{
				"query": cfGraphQLQuery,
				"variables": map[string]interface{}{
					"AccountID": accID,
					"filter": map[string]string{
						"datetime_geq": dayStartIso,
						"datetime_leq": nowIso,
					},
				},
			}
			wpBytes, wpErr = doCFPost(client, token, reqBody)
		}()

		// 2. KV
		go func() {
			defer wg.Done()
			reqBody := map[string]interface{}{
				"query": cfKvGraphQLQuery,
				"variables": map[string]interface{}{
					"accountTag":   accID,
					"dayStart":     dayStartDate,
					"dateEnd":       dateEnd,
					"storageStart": monthStartDate,
				},
			}
			kvBytes, kvErr = doCFPost(client, token, reqBody)
		}()

		// 3. D1
		go func() {
			defer wg.Done()
			reqBody := map[string]interface{}{
				"query": cfD1GraphQLQuery,
				"variables": map[string]interface{}{
					"accountTag":   accID,
					"dayStart":     dayStartDate,
					"dateEnd":       dateEnd,
					"storageStart": monthStartDate,
				},
			}
			d1Bytes, d1Err = doCFPost(client, token, reqBody)
		}()

		// 4. R2
		go func() {
			defer wg.Done()
			reqBody := map[string]interface{}{
				"query": cfR2GraphQLQuery,
				"variables": map[string]interface{}{
					"accountTag": accID,
					"monthStart": monthStartIso,
					"now":        nowIso,
				},
			}
			r2Bytes, r2Err = doCFPost(client, token, reqBody)
		}()

		wg.Wait()

		// 解析 Workers & Pages
		var workersReq, pagesReq int64
		var errStr string
		if wpErr == nil && len(wpBytes) > 0 {
			var gqlResp struct {
				Data struct {
					Viewer struct {
						Accounts []struct {
							WorkersInvocationsAdaptive []struct {
								Sum struct {
									Requests int64 `json:"requests"`
								} `json:"sum"`
							} `json:"workersInvocationsAdaptive"`
							PagesFunctionsInvocationsAdaptiveGroups []struct {
								Sum struct {
									Requests int64 `json:"requests"`
								} `json:"sum"`
							} `json:"pagesFunctionsInvocationsAdaptiveGroups"`
						} `json:"accounts"`
					} `json:"viewer"`
				} `json:"data"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			_ = json.Unmarshal(wpBytes, &gqlResp)
			if len(gqlResp.Data.Viewer.Accounts) > 0 {
				accItem := gqlResp.Data.Viewer.Accounts[0]
				for _, w := range accItem.WorkersInvocationsAdaptive {
					workersReq += w.Sum.Requests
				}
				for _, p := range accItem.PagesFunctionsInvocationsAdaptiveGroups {
					pagesReq += p.Sum.Requests
				}
			}
			if len(gqlResp.Errors) > 0 {
				errStr = gqlResp.Errors[0].Message
			}
		} else if wpErr != nil {
			errStr = wpErr.Error()
		}

		// 解析 KV
		var kvReads, kvWrites, kvDeletes, kvLists, kvOperations int64
		var kvNamespaces, kvKeys, kvStorageBytes int64
		if kvErr == nil && len(kvBytes) > 0 {
			var resp struct {
				Data struct {
					Viewer struct {
						Accounts []struct {
							KVOperationsAdaptiveGroups []struct {
								Sum struct {
									Requests int64 `json:"requests"`
								} `json:"sum"`
								Dimensions struct {
									ActionType string `json:"actionType"`
								} `json:"dimensions"`
							} `json:"kvOperationsAdaptiveGroups"`
							KVStorageAdaptiveGroups []struct {
								Max struct {
									KeyCount  int64 `json:"keyCount"`
									ByteCount int64 `json:"byteCount"`
								} `json:"max"`
								Dimensions struct {
									Date        string `json:"date"`
									NamespaceID string `json:"namespaceId"`
								} `json:"dimensions"`
							} `json:"kvStorageAdaptiveGroups"`
						} `json:"accounts"`
					} `json:"viewer"`
				} `json:"data"`
			}
			_ = json.Unmarshal(kvBytes, &resp)
			if len(resp.Data.Viewer.Accounts) > 0 {
				accItem := resp.Data.Viewer.Accounts[0]
				for _, g := range accItem.KVOperationsAdaptiveGroups {
					reqs := g.Sum.Requests
					act := strings.ToLower(g.Dimensions.ActionType)
					kvOperations += reqs
					if strings.Contains(act, "read") {
						kvReads += reqs
					} else if strings.Contains(act, "write") {
						kvWrites += reqs
					} else if strings.Contains(act, "delete") {
						kvDeletes += reqs
					} else if strings.Contains(act, "list") {
						kvLists += reqs
					}
				}

				latestMap := make(map[string]struct {
					date  string
					keys  int64
					bytes int64
				})
				for _, g := range accItem.KVStorageAdaptiveGroups {
					ns := g.Dimensions.NamespaceID
					if ns == "" {
						ns = "__acc"
					}
					cur, ok := latestMap[ns]
					if !ok || g.Dimensions.Date >= cur.date {
						latestMap[ns] = struct {
							date  string
							keys  int64
							bytes int64
						}{
							date:  g.Dimensions.Date,
							keys:  g.Max.KeyCount,
							bytes: g.Max.ByteCount,
						}
					}
				}
				kvNamespaces = int64(len(latestMap))
				for _, v := range latestMap {
					kvKeys += v.keys
					kvStorageBytes += v.bytes
				}
			}
		}

		// 解析 D1
		var d1RowsRead, d1RowsWritten, d1ReadQueries, d1WriteQueries int64
		var d1Databases, d1StorageBytes int64
		if d1Err == nil && len(d1Bytes) > 0 {
			var resp struct {
				Data struct {
					Viewer struct {
						Accounts []struct {
							D1AnalyticsAdaptiveGroups []struct {
								Sum struct {
									RowsRead     int64 `json:"rowsRead"`
									RowsWritten  int64 `json:"rowsWritten"`
									ReadQueries  int64 `json:"readQueries"`
									WriteQueries int64 `json:"writeQueries"`
								} `json:"sum"`
								Dimensions struct {
									Date       string `json:"date"`
									DatabaseID string `json:"databaseId"`
								} `json:"dimensions"`
							} `json:"d1AnalyticsAdaptiveGroups"`
							D1StorageAdaptiveGroups []struct {
								Max struct {
									DatabaseSizeBytes int64 `json:"databaseSizeBytes"`
								} `json:"max"`
								Dimensions struct {
									Date       string `json:"date"`
									DatabaseID string `json:"databaseId"`
								} `json:"dimensions"`
							} `json:"d1StorageAdaptiveGroups"`
						} `json:"accounts"`
					} `json:"viewer"`
				} `json:"data"`
			}
			_ = json.Unmarshal(d1Bytes, &resp)
			if len(resp.Data.Viewer.Accounts) > 0 {
				accItem := resp.Data.Viewer.Accounts[0]
				for _, g := range accItem.D1AnalyticsAdaptiveGroups {
					d1RowsRead += g.Sum.RowsRead
					d1RowsWritten += g.Sum.RowsWritten
					d1ReadQueries += g.Sum.ReadQueries
					d1WriteQueries += g.Sum.WriteQueries
				}

				latestMap := make(map[string]struct {
					date  string
					bytes int64
				})
				for _, g := range accItem.D1StorageAdaptiveGroups {
					db := g.Dimensions.DatabaseID
					if db == "" {
						db = "__acc"
					}
					cur, ok := latestMap[db]
					if !ok || g.Dimensions.Date >= cur.date {
						latestMap[db] = struct {
							date  string
							bytes int64
						}{
							date:  g.Dimensions.Date,
							bytes: g.Max.DatabaseSizeBytes,
						}
					}
				}
				d1Databases = int64(len(latestMap))
				for _, v := range latestMap {
					d1StorageBytes += v.bytes
				}
			}
		}

		// 解析 R2
		var r2ClassA, r2ClassB, r2Operations int64
		var r2Buckets, r2Objects, r2StorageBytes int64
		if r2Err == nil && len(r2Bytes) > 0 {
			var resp struct {
				Data struct {
					Viewer struct {
						Accounts []struct {
							R2OperationsAdaptiveGroups []struct {
								Sum struct {
									Requests int64 `json:"requests"`
								} `json:"sum"`
								Dimensions struct {
									ActionType   string `json:"actionType"`
									ActionStatus string `json:"actionStatus"`
								} `json:"dimensions"`
							} `json:"r2OperationsAdaptiveGroups"`
							R2StorageAdaptiveGroups []struct {
								Max struct {
									ObjectCount  int64 `json:"objectCount"`
									UploadCount  int64 `json:"uploadCount"`
									PayloadSize  int64 `json:"payloadSize"`
									MetadataSize int64 `json:"metadataSize"`
								} `json:"max"`
								Dimensions struct {
									Datetime   string `json:"datetime"`
									BucketName string `json:"bucketName"`
								} `json:"dimensions"`
							} `json:"r2StorageAdaptiveGroups"`
						} `json:"accounts"`
					} `json:"viewer"`
				} `json:"data"`
			}
			_ = json.Unmarshal(r2Bytes, &resp)
			if len(resp.Data.Viewer.Accounts) > 0 {
				accItem := resp.Data.Viewer.Accounts[0]
				for _, g := range accItem.R2OperationsAdaptiveGroups {
					status := strings.ToLower(g.Dimensions.ActionStatus)
					if status != "" && status != "success" {
						continue
					}
					reqs := g.Sum.Requests
					act := strings.ToLower(g.Dimensions.ActionType)
					r2Operations += reqs
					if r2ClassAActions[act] {
						r2ClassA += reqs
					} else if r2ClassBActions[act] {
						r2ClassB += reqs
					}
				}

				latestMap := make(map[string]struct {
					datetime string
					objects  int64
					bytes    int64
				})
				for _, g := range accItem.R2StorageAdaptiveGroups {
					b := g.Dimensions.BucketName
					if b == "" {
						b = "__acc"
					}
					cur, ok := latestMap[b]
					if !ok || g.Dimensions.Datetime >= cur.datetime {
						latestMap[b] = struct {
							datetime string
							objects  int64
							bytes    int64
						}{
							datetime: g.Dimensions.Datetime,
							objects:  g.Max.ObjectCount,
							bytes:    g.Max.PayloadSize + g.Max.MetadataSize,
						}
					}
				}
				r2Buckets = int64(len(latestMap))
				for _, v := range latestMap {
					r2Objects += v.objects
					r2StorageBytes += v.bytes
				}
			}
		}

		// REST API Fallback：若当天没有指标统计，但子账户下实际存在空库/桶/空间，做快速探测
		if kvNamespaces == 0 {
			if cnt := tryCountRestList(client, token, fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces", accID)); cnt > 0 {
				kvNamespaces = int64(cnt)
			}
		}
		if d1Databases == 0 {
			if cnt := tryCountRestList(client, token, fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/d1/database", accID)); cnt > 0 {
				d1Databases = int64(cnt)
			}
		}
		if r2Buckets == 0 {
			if cnt := tryCountRestList(client, token, fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/r2/buckets", accID)); cnt > 0 {
				r2Buckets = int64(cnt)
			}
		}

		total := workersReq + pagesReq
		maxQuota := int64(100000)
		pct := float64(total) / float64(maxQuota) * 100.0

		resources := map[string]interface{}{
			"kv": map[string]interface{}{
				"namespaces":        kvNamespaces,
				"reads":             kvReads,
				"readsLimit":        100000,
				"writes":            kvWrites,
				"writesLimit":       1000,
				"deletes":           kvDeletes,
				"lists":             kvLists,
				"deletesLimit":      1000,
				"listsLimit":        1000,
				"operations":        kvOperations,
				"storageBytes":      kvStorageBytes,
				"storageLimitBytes": 1073741824,
				"keys":              kvKeys,
				"period":            "day",
			},
			"d1": map[string]interface{}{
				"databases":         d1Databases,
				"rowsRead":          d1RowsRead,
				"rowsReadLimit":     5000000,
				"rowsWritten":       d1RowsWritten,
				"rowsWrittenLimit":  100000,
				"readQueries":       d1ReadQueries,
				"writeQueries":      d1WriteQueries,
				"storageBytes":      d1StorageBytes,
				"storageLimitBytes": 5368709120,
				"period":            "day",
			},
			"r2": map[string]interface{}{
				"buckets":           r2Buckets,
				"classA":            r2ClassA,
				"classALimit":       1000000,
				"classB":            r2ClassB,
				"classBLimit":       10000000,
				"storageBytes":      r2StorageBytes,
				"storageLimitBytes": 10737418240,
				"operations":        r2Operations,
				"objects":           r2Objects,
				"period":            "month",
			},
		}

		briefList = append(briefList, model.CFAccountBrief{
			ID:         acc.ID,
			Name:       name,
			AccountID:  accID,
			UpdateTime: time.Now().UTC().Format(time.RFC3339),
			Workers:    workersReq,
			Pages:      pagesReq,
			Total:      total,
			Max:        maxQuota,
			Percent:    pct,
			Error:      errStr,
			Resources:  resources,
		})
	}

	logger.Infof("[CF Quota Direct] 成功通过官方 API 轮询 %d 个子账户配额与云资源指标", len(briefList))

	return model.CFAccountsResult{
		OK:       true,
		Mode:     "direct",
		Count:    len(briefList),
		Accounts: briefList,
	}
}

func (m *Manager) fetchUsagePanel(cfg model.Config) model.CFAccountsResult {
	panelURL := cfg.CFPanel.URL
	if panelURL == "" {
		panelURL = cfg.CFPanelURL
	}
	if panelURL == "" {
		panelURL = "https://usagepanel.pages.dev"
	}
	panelURL = strings.TrimSuffix(panelURL, "/")

	username := strings.TrimSpace(cfg.CFPanel.Username)
	password := cfg.CFPanel.Password
	if username == "" || password == "" {
		return model.CFAccountsResult{
			OK:       false,
			NeedAuth: true,
			Message:  "尚未配置 CF 面板管理员凭据，请在【配置面板凭据】中填入账号与密码",
		}
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return model.CFAccountsResult{OK: false, Error: err.Error()}
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
	}

	// 1. POST /api/login 获取会话凭证 Cookie
	loginPayload, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})

	loginURL := fmt.Sprintf("%s/api/login", panelURL)
	loginReq, err := http.NewRequest("POST", loginURL, bytes.NewReader(loginPayload))
	if err != nil {
		return model.CFAccountsResult{OK: false, Error: err.Error()}
	}
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("User-Agent", "EdgeTunnel-LB-Go/2.0")

	loginResp, err := client.Do(loginReq)
	if err != nil {
		return model.CFAccountsResult{OK: false, Error: fmt.Sprintf("面板登录请求失败: %v", err)}
	}
	defer loginResp.Body.Close()

	if loginResp.StatusCode != http.StatusOK {
		return model.CFAccountsResult{OK: false, Error: fmt.Sprintf("面板登录认证失败 (HTTP %d)", loginResp.StatusCode)}
	}

	// 2. 带上登录 Cookie 请求 /admin/config.json 获取完整子账户列表与额度
	configURL := fmt.Sprintf("%s/admin/config.json", panelURL)
	configReq, err := http.NewRequest("GET", configURL, nil)
	if err != nil {
		return model.CFAccountsResult{OK: false, Error: err.Error()}
	}
	configReq.Header.Set("User-Agent", "EdgeTunnel-LB-Go/2.0")

	configResp, err := client.Do(configReq)
	if err != nil {
		return model.CFAccountsResult{OK: false, Error: fmt.Sprintf("获取面板子账户数据失败: %v", err)}
	}
	defer configResp.Body.Close()

	if configResp.StatusCode != http.StatusOK {
		return model.CFAccountsResult{OK: false, Error: fmt.Sprintf("获取子账户数据返回 HTTP %d", configResp.StatusCode)}
	}

	bodyBytes, err := io.ReadAll(configResp.Body)
	if err != nil {
		return model.CFAccountsResult{OK: false, Error: err.Error()}
	}

	type usageInfo struct {
		Pages     int64                  `json:"pages"`
		Workers   int64                  `json:"workers"`
		Total     int64                  `json:"total"`
		Max       int64                  `json:"max"`
		Resources map[string]interface{} `json:"resources"`
	}

	type rawAccount struct {
		ID         interface{} `json:"ID"`
		Name       string      `json:"Name"`
		AccountID  string      `json:"AccountID"`
		UpdateTime interface{} `json:"UpdateTime"`
		Usage      usageInfo   `json:"Usage"`
	}

	var rawList []rawAccount
	if err := json.Unmarshal(bodyBytes, &rawList); err != nil {
		return model.CFAccountsResult{OK: false, Error: fmt.Sprintf("解析子账户数据失败: %v", err)}
	}

	var briefList []model.CFAccountBrief
	for _, a := range rawList {
		maxQ := a.Usage.Max
		if maxQ <= 0 {
			maxQ = 100000
		}
		total := a.Usage.Total
		if total <= 0 {
			total = a.Usage.Pages + a.Usage.Workers
		}
		pct := float64(total) / float64(maxQ) * 100.0

		res := a.Usage.Resources
		if res == nil {
			res = buildDefaultResources()
		}

		var timeStr string
		if num, ok := a.UpdateTime.(float64); ok && num > 0 {
			sec := int64(num) / 1000
			nsec := (int64(num) % 1000) * 1000000
			timeStr = time.Unix(sec, nsec).UTC().Format(time.RFC3339)
		} else if s, ok := a.UpdateTime.(string); ok && s != "" {
			timeStr = s
		} else {
			timeStr = time.Now().UTC().Format(time.RFC3339)
		}

		briefList = append(briefList, model.CFAccountBrief{
			ID:         fmt.Sprintf("%v", a.ID),
			Name:       a.Name,
			AccountID:  a.AccountID,
			UpdateTime: timeStr,
			Workers:    a.Usage.Workers,
			Pages:      a.Usage.Pages,
			Total:      total,
			Max:        maxQ,
			Percent:    pct,
			Resources:  res,
		})
	}

	logger.Infof("[CF Quota Patrol] 成功拉取 UsagePanel %d 个子账户额度数据与资源详情", len(briefList))

	return model.CFAccountsResult{
		OK:       true,
		Mode:     "usagepanel",
		Count:    len(briefList),
		Accounts: briefList,
	}
}

func (m *Manager) checkAndTriggerQuotaAlert(accounts []model.CFAccountBrief, cfg model.Config) {
	threshold := float64(cfg.CFAlertThreshold)
	if threshold <= 0 {
		threshold = 80
	}
	todayStr := time.Now().Format("2006-01-02")

	for _, acc := range accounts {
		if acc.Error != "" {
			continue
		}
		alertKey := fmt.Sprintf("%s-%s-%.0f", todayStr, acc.ID, threshold)
		if _, alerted := m.alertedToday[alertKey]; !alerted && acc.Percent >= threshold {
			m.alertedToday[alertKey] = struct{}{}

			msg := fmt.Sprintf("⚠️ *【Cloudflare 额度预警】*\n\n"+
				"• *账户名称*: %s\n"+
				"• *今日请求*: %d / %d (%.1f%%)\n"+
				"• *Workers*: %d | *Pages*: %d\n"+
				"• *已达告警阈值*: %.0f%%\n\n"+
				"💡 请注意分流均衡或适时切换优选后端。",
				acc.Name, acc.Total, acc.Max, acc.Percent, acc.Workers, acc.Pages, threshold)

			if sender := notify.GetTelegram(); sender != nil {
				go func() {
					_ = sender.SendRawText(msg)
				}()
			}
			logger.Warnf("[CF Quota Alert] 账户 %s 达到 %.1f%% (设定阈值: %.0f%%)，已触发告警推送", acc.Name, acc.Percent, threshold)
		}
	}
}
