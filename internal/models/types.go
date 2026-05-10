package models

import "time"

// ========== 数据库实体 ==========

type ProxyConfig struct {
	Id        int64     `json:"id"`
	ProxyCode string    `json:"proxyCode"`
	ProxyName string    `json:"proxyName"`
	TargetUrl string    `json:"targetUrl"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RequestSummary 轻量元数据，列表查询用
type RequestSummary struct {
	RequestId       string `json:"requestId"`
	StartTime       int64  `json:"startTime"`
	EndTime         int64  `json:"endTime,omitempty"`
	Duration        int64  `json:"duration,omitempty"`
	ProxyCode       string `json:"proxyCode"`
	Method          string `json:"method"`
	Path            string `json:"path"`
	TargetUrl       string `json:"targetUrl"`
	Status          string `json:"status"` // processing/success/degraded/error/intercepted
	StatusCode      int    `json:"statusCode,omitempty"`
	ResponseChars   int    `json:"responseChars"`
	RequestChars    int    `json:"requestChars"`
	Ttft            int64  `json:"ttft,omitempty"` // Time To First Token (ms)
	Title           string `json:"title,omitempty"`
	Model           string `json:"model,omitempty"`
	Stream          bool   `json:"stream"`
	ToolCount       int    `json:"toolCount,omitempty"`
	Error           string `json:"error,omitempty"`
	StatusReason    string `json:"statusReason,omitempty"`
}

// RequestPayload 完整请求数据，按需加载
type RequestPayload struct {
	RequestId     string           `json:"requestId"`
	ProxyCode     string           `json:"proxyCode"`
	Method        string           `json:"method"`
	Path          string           `json:"path"`
	TargetUrl     string           `json:"targetUrl"`
	OrigHeaders   map[string]string `json:"origHeaders"`
	OrigBody      string           `json:"origBody,omitempty"`
	TargetHeaders map[string]string `json:"targetHeaders,omitempty"`
	TargetBody    string           `json:"targetBody,omitempty"`
	ResponseBody  string           `json:"responseBody,omitempty"`
	StatusCode    int              `json:"statusCode"`
	Duration      int64            `json:"duration"`
	Logs          []LogEntry       `json:"logs,omitempty"`
}

// LogEntry 结构化日志事件
type LogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Phase     string `json:"phase"`  // receive/proxy/stream/complete/error
	Level     string `json:"level"`  // info/warn/error
	Source    string `json:"source"` // proxy/parser
	Message   string `json:"message"`
	Details   string `json:"details,omitempty"`
}

// ========== API 响应 ==========

type PageResult struct {
	Data  interface{} `json:"data"`
	Total int         `json:"total"`
}

type StatsResult struct {
	TotalRequests   int   `json:"totalRequests"`
	SuccessCount    int   `json:"successCount"`
	DegradedCount   int   `json:"degradedCount"`
	ErrorCount      int   `json:"errorCount"`
	ProcessingCount int   `json:"processingCount"`
	InterceptedCount int  `json:"interceptedCount"`
	AvgResponseTime int   `json:"avgResponseTime"`
	AvgTtft         int   `json:"avgTtft"`
}
