package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"model-proxy-go/internal/models"
)

var DB *sql.DB

// configCache 给 ConfigByCode 做 TTL 缓存，避免每次代理请求都打一次 SQLite。
// 配置变更（Add/Update/Delete）会立刻清缓存。
type cachedConfig struct {
	config    *models.ProxyConfig
	expiresAt time.Time
}

var (
	configCache    sync.Map
	configCacheTTL = 30 * time.Second
)

func invalidateConfigCache() {
	configCache.Range(func(k, _ interface{}) bool {
		configCache.Delete(k)
		return true
	})
}

func Init(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var err error
	DB, err = sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return err
	}
	DB.SetMaxOpenConns(1) // SQLite 单写

	return migrate()
}

func migrate() error {
	_, err := DB.Exec(`
		CREATE TABLE IF NOT EXISTS proxy_config (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			proxy_code TEXT NOT NULL UNIQUE,
			proxy_name TEXT NOT NULL,
			target_url TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS requests (
			request_id TEXT PRIMARY KEY,
			timestamp INTEGER NOT NULL,
			summary_json TEXT NOT NULL,
			payload_json TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_timestamp ON requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_proxy_code ON requests(summary_json);

		INSERT OR IGNORE INTO proxy_config (proxy_code, proxy_name, target_url)
		VALUES ('glm', 'GLM大模型', 'https://open.bigmodel.cn');
	`)
	return err
}

func Close() {
	if DB != nil {
		DB.Close()
	}
}

// ========== ProxyConfig CRUD ==========

func ConfigList() ([]models.ProxyConfig, error) {
	rows, err := DB.Query("SELECT id, proxy_code, proxy_name, target_url, created_at, updated_at FROM proxy_config ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []models.ProxyConfig
	for rows.Next() {
		var c models.ProxyConfig
		if err := rows.Scan(&c.Id, &c.ProxyCode, &c.ProxyName, &c.TargetUrl, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, nil
}

func ConfigByCode(code string) (*models.ProxyConfig, error) {
	// 缓存命中
	if v, ok := configCache.Load(code); ok {
		cc := v.(cachedConfig)
		if time.Now().Before(cc.expiresAt) {
			return cc.config, nil
		}
		configCache.Delete(code)
	}

	var c models.ProxyConfig
	err := DB.QueryRow(
		"SELECT id, proxy_code, proxy_name, target_url, created_at, updated_at FROM proxy_config WHERE proxy_code = ?", code,
	).Scan(&c.Id, &c.ProxyCode, &c.ProxyName, &c.TargetUrl, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		// 未命中也缓存一段时间，避免坏 code 反复打 DB
		configCache.Store(code, cachedConfig{config: nil, expiresAt: time.Now().Add(configCacheTTL)})
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	configCache.Store(code, cachedConfig{config: &c, expiresAt: time.Now().Add(configCacheTTL)})
	return &c, nil
}

func ConfigAdd(c *models.ProxyConfig) error {
	result, err := DB.Exec(
		"INSERT INTO proxy_config (proxy_code, proxy_name, target_url) VALUES (?, ?, ?)",
		c.ProxyCode, c.ProxyName, c.TargetUrl,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	c.Id = id
	invalidateConfigCache()
	return nil
}

func ConfigUpdate(c *models.ProxyConfig) error {
	_, err := DB.Exec(
		"UPDATE proxy_config SET proxy_name = ?, target_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		c.ProxyName, c.TargetUrl, c.Id,
	)
	if err == nil {
		invalidateConfigCache()
	}
	return err
}

func ConfigDelete(id int64) error {
	_, err := DB.Exec("DELETE FROM proxy_config WHERE id = ?", id)
	if err == nil {
		invalidateConfigCache()
	}
	return err
}

// ========== Request 写入 ==========

func InsertRequest(summary *models.RequestSummary, payload *models.RequestPayload) error {
	summaryJson, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	payloadJson, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return ExecInsertRequest(summary.RequestId, summary.StartTime, summaryJson, payloadJson)
}

// ExecInsertRequest 接受预序列化的 JSON，供异步调用使用
func ExecInsertRequest(requestId string, startTime int64, summaryJson, payloadJson []byte) error {
	_, err := DB.Exec(
		"INSERT OR REPLACE INTO requests (request_id, timestamp, summary_json, payload_json) VALUES (?, ?, ?, ?)",
		requestId, startTime, string(summaryJson), string(payloadJson),
	)
	return err
}

func UpdateSummary(summary *models.RequestSummary) error {
	summaryJson, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = DB.Exec(
		"UPDATE requests SET summary_json = ? WHERE request_id = ?",
		string(summaryJson), summary.RequestId,
	)
	return err
}

// ========== Request 查询 ==========

func GetSummaries(limit, offset int, keyword, status string) ([]models.RequestSummary, int, error) {
	where := "1=1"
	args := []interface{}{}
	if keyword != "" {
		where += " AND (request_id LIKE ? OR summary_json LIKE ?)"
		kw := "%" + keyword + "%"
		args = append(args, kw, kw)
	}
	if status != "" && status != "all" {
		where += " AND json_extract(summary_json, '$.status') = ?"
		args = append(args, status)
	}

	// count
	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM requests WHERE %s", where)
	if err := DB.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// query
	querySQL := fmt.Sprintf("SELECT summary_json FROM requests WHERE %s ORDER BY timestamp DESC LIMIT ? OFFSET ?", where)
	allArgs := append(args, limit, offset)
	rows, err := DB.Query(querySQL, allArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var summaries []models.RequestSummary
	for rows.Next() {
		var jsonStr string
		if err := rows.Scan(&jsonStr); err != nil {
			continue
		}
		var s models.RequestSummary
		if json.Unmarshal([]byte(jsonStr), &s) == nil {
			summaries = append(summaries, s)
		}
	}
	if summaries == nil {
		summaries = []models.RequestSummary{}
	}
	return summaries, total, nil
}

func GetPayload(requestId string) (*models.RequestPayload, error) {
	var jsonStr string
	err := DB.QueryRow("SELECT payload_json FROM requests WHERE request_id = ?", requestId).Scan(&jsonStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p models.RequestPayload
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func DeleteRequest(id string) error {
	_, err := DB.Exec("DELETE FROM requests WHERE request_id = ?", id)
	return err
}

// ========== 统计 ==========

func GetStats() (*models.StatsResult, error) {
	var r models.StatsResult
	err := DB.QueryRow(`
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN json_extract(summary_json,'$.status')='success'     THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN json_extract(summary_json,'$.status')='degraded'    THEN 1 ELSE 0 END) as degraded,
			SUM(CASE WHEN json_extract(summary_json,'$.status')='error'       THEN 1 ELSE 0 END) as error,
			SUM(CASE WHEN json_extract(summary_json,'$.status')='intercepted' THEN 1 ELSE 0 END) as intercepted,
			SUM(CASE WHEN json_extract(summary_json,'$.status')='processing'  THEN 1 ELSE 0 END) as processing,
			AVG(CASE WHEN json_extract(summary_json,'$.duration') IS NOT NULL
				THEN json_extract(summary_json,'$.duration') END) as avgDuration,
			AVG(CASE WHEN json_extract(summary_json,'$.ttft') IS NOT NULL
				THEN json_extract(summary_json,'$.ttft') END) as avgTTFT
		FROM requests
	`).Scan(&r.TotalRequests, &r.SuccessCount, &r.DegradedCount,
		&r.ErrorCount, &r.InterceptedCount, &r.ProcessingCount,
		&r.AvgResponseTime, &r.AvgTtft)
	if err != nil {
		return &r, nil
	}
	return &r, nil
}

// ========== 训练数据导出 ==========

type ExportPayload struct {
	RequestId    string `json:"requestId"`
	ProxyCode    string `json:"proxyCode"`
	OrigBody     string `json:"origBody"`
	ResponseBody string `json:"responseBody"`
}

func GetPayloadsForExport(status, since, limit string) ([]ExportPayload, error) {
	where := "1=1"
	args := []interface{}{}
	if status != "" && status != "all" {
		where += " AND json_extract(summary_json, '$.status') = ?"
		args = append(args, status)
	}
	if since != "" {
		where += " AND timestamp >= ?"
		// since 是毫秒时间戳，转整数跟数据库 INTEGER 对齐
		var sinceMs int64
		fmt.Sscanf(since, "%d", &sinceMs)
		args = append(args, sinceMs)
	}
	lim := 500
	fmt.Sscanf(limit, "%d", &lim)
	args = append(args, lim)
	sql := fmt.Sprintf(`SELECT request_id,
		json_extract(payload_json, '$.origBody') as origBody,
		json_extract(payload_json, '$.responseBody') as responseBody
		FROM requests WHERE %s ORDER BY timestamp DESC LIMIT ?`, where)
	rows, err := DB.Query(sql, args...)
	if err != nil {
		fmt.Printf("[DB] Query error: %v\n", err)
		return nil, err
	}
	defer rows.Close()
	var result []ExportPayload
	for rows.Next() {
		var p ExportPayload
		var origBody, respBody *string
		if err := rows.Scan(&p.RequestId, &origBody, &respBody); err != nil {
			continue
		}
		if origBody != nil {
			p.OrigBody = *origBody
		}
		if respBody != nil {
			p.ResponseBody = *respBody
		}
		result = append(result, p)
	}
	if result == nil {
		result = []ExportPayload{}
	}
	return result, nil
}
