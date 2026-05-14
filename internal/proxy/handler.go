package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"model-proxy-go/internal/db"
	"model-proxy-go/internal/models"
)

// proxyTransport 是共享的 HTTP Transport，配置了合理的连接池和超时。
// 避免使用 http.DefaultTransport 的默认配置（MaxIdleConnsPerHost=2 太小，无超时保护）。
var proxyTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,

	// 连接池 — 增大可复用连接数，减少 "bad record MAC" 等复用失败
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   20,
	MaxConnsPerHost:       50,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 120 * time.Second, // 等待目标响应头的超时
	ExpectContinueTimeout: 1 * time.Second,

	// TLS 配置 — 使用 Go 默认的 TLS 设置（TLS 1.2+，系统 CA 池）
	TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
	},
}

const proxyRequestTimeout = 300 * time.Second

// Handler 创建代理 HTTP Handler
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		requestId := genRequestId()

		// 提取 proxyCode: /api/{proxyCode}/...
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		parts := strings.SplitN(path, "/", 2)
		proxyCode := parts[0]
		remainingPath := ""
		if len(parts) > 1 {
			remainingPath = "/" + parts[1]
		}

		// 查配置
		config, err := db.ConfigByCode(proxyCode)
		if err != nil || config == nil {
			http.NotFound(w, r)
			return
		}

		// 读原始 body
		origBodyBytes, _ := io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(origBodyBytes))

		origHeaders := sanitizeHeaders(flattenHeaders(r.Header))

		// 构建目标 URL
		targetUrl := strings.TrimRight(config.TargetUrl, "/") + remainingPath
		if r.URL.RawQuery != "" {
			targetUrl += "?" + r.URL.RawQuery
		}

		target, err := url.Parse(strings.TrimRight(config.TargetUrl, "/"))
	if err != nil || target == nil || target.Host == "" {
		http.Error(w, "Bad Gateway: invalid target URL", http.StatusBadGateway)
		return
	}

		// 收集目标 headers
		targetHeaders := make(map[string]string)
		for k, v := range r.Header {
			if strings.EqualFold(k, "host") {
				continue
			}
			targetHeaders[k] = strings.Join(v, ", ")
		}
		targetHeaders["Host"] = target.Host
		targetHeaders = sanitizeHeaders(targetHeaders)

		// 构建 summary
		summary := &models.RequestSummary{
			RequestId:    requestId,
			StartTime:    startTime.UnixMilli(),
			ProxyCode:    proxyCode,
			Method:       r.Method,
			Path:         r.URL.Path,
			TargetUrl:    targetUrl,
			Status:       "processing",
			Stream:        strings.Contains(r.Header.Get("Accept"), "text/event-stream"),
			RequestChars: len(origBodyBytes),
			Model:        extractModel(string(origBodyBytes)),
		}
		if summary.Model == "" {
			summary.Model = proxyCode
		}

		// 构建 payload
		payload := &models.RequestPayload{
			RequestId:     requestId,
			ProxyCode:     proxyCode,
			Method:        r.Method,
			Path:          r.URL.Path,
			TargetUrl:     targetUrl,
			OrigHeaders:   origHeaders,
			OrigBody:      sanitizeBody(string(origBodyBytes)),
			TargetHeaders: targetHeaders,
			TargetBody:    string(origBodyBytes),
		}

		addLog(payload,models.LogEntry{
			Timestamp: time.Now().UnixMilli(),
			Phase:     "receive",
			Level:     "info",
			Source:    "proxy",
			Message:   fmt.Sprintf("收到请求 %s %s → %s", r.Method, r.URL.Path, targetUrl),
			Details:   fmt.Sprintf("proxyCode=%s requestChars=%d stream=%v", proxyCode, len(origBodyBytes), summary.Stream),
		})

		// 保存初始状态
		db.InsertRequest(summary, payload)
		broadcastSummary(summary)

		// 创建反向代理
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = proxyTransport

		// 请求级超时：通过 context 控制上游请求的最大执行时间
		reqCtx, reqCancel := context.WithTimeout(r.Context(), proxyRequestTimeout)
		defer reqCancel()
		*r = *r.WithContext(reqCtx)

		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// 拼接 target 的基础路径 + 剩余路径
			joinedPath := strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(remainingPath, "/")
			joinedPath = strings.TrimRight(joinedPath, "/")
			if joinedPath == "" {
				joinedPath = "/"
			}
			req.URL.Path = joinedPath
			req.URL.RawQuery = r.URL.RawQuery
			for k, vs := range r.Header {
				if strings.EqualFold(k, "host") {
					continue
				}
				req.Header[k] = vs
			}
		}

		var responseBody bytes.Buffer
		var ttftSet bool
		var ttft time.Duration
		var mu sync.Mutex
		firstByte := make(chan struct{}, 1)

		proxy.ModifyResponse = func(resp *http.Response) error {
			statusCode := resp.StatusCode
			mu.Lock()
			payload.StatusCode = statusCode
			mu.Unlock()

			addLog(payload,models.LogEntry{
				Timestamp: time.Now().UnixMilli(),
				Phase:     "proxy",
				Level:     "info",
				Source:    "proxy",
				Message:   fmt.Sprintf("目标响应 %d", statusCode),
			})

			// 包装 body 以捕获响应内容
			body := resp.Body
			if body == nil {
				body = io.NopCloser(strings.NewReader(""))
			}
			resp.Body = &captureReader{
				ReadCloser: body,
				buf:        &responseBody,
				onFirstByte: func() {
					mu.Lock()
					if !ttftSet {
						ttft = time.Since(startTime)
						ttftSet = true
						summary.Ttft = ttft.Milliseconds()
					}
					mu.Unlock()
					select {
					case firstByte <- struct{}{}:
					default:
					}
				},
			}
			return nil
		}

		addLog(payload,models.LogEntry{
			Timestamp: time.Now().UnixMilli(),
			Phase:     "proxy",
			Level:     "info",
			Source:    "proxy",
			Message:   fmt.Sprintf("转发到 %s", targetUrl),
		})

		proxy.ServeHTTP(w, r)

		// 检查是否为 context 取消导致的错误
		if reqCtx.Err() != nil {
			addLog(payload, models.LogEntry{
				Timestamp: time.Now().UnixMilli(),
				Phase:     "proxy",
				Level:     "warn",
				Source:    "proxy",
				Message:   fmt.Sprintf("上游请求被取消: %v", reqCtx.Err()),
			})
		}

		// 请求完成
		duration := time.Since(startTime)
		summary.EndTime = time.Now().UnixMilli()
		summary.Duration = duration.Milliseconds()
		summary.ResponseChars = responseBody.Len()
		payload.Duration = duration.Milliseconds()
		payload.ResponseBody = sanitizeBody(responseBody.String())

		// 判断状态
		if payload.StatusCode >= 200 && payload.StatusCode < 300 {
			summary.Status = "success"
		} else if payload.StatusCode >= 400 && summary.ResponseChars > 0 {
			summary.Status = "degraded"
		} else if payload.StatusCode >= 400 {
			summary.Status = "error"
		} else {
			summary.Status = "success"
		}

		addLog(payload,models.LogEntry{
			Timestamp: time.Now().UnixMilli(),
			Phase:     "complete",
			Level:     "info",
			Source:    "proxy",
			Message:   fmt.Sprintf("完成 %dms, status=%s, chars=%d", duration.Milliseconds(), summary.Status, summary.ResponseChars),
			Details:   fmt.Sprintf("statusCode=%d ttft=%dms", payload.StatusCode, summary.Ttft),
		})

		// 更新数据库
		db.InsertRequest(summary, payload)
		broadcastSummary(summary)
	}
}

func genRequestId() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func flattenHeaders(h http.Header) map[string]string {
	m := make(map[string]string)
	for k, vs := range h {
		m[k] = strings.Join(vs, ", ")
	}
	return m
}

// 脱敏：移除 Authorization 等敏感头中的实际值
func sanitizeBody(s string) string {
	return s
}

// captureReader 在首次读取时触发回调
type captureReader struct {
	io.ReadCloser
	buf         *bytes.Buffer
	onFirstByte func()
	once        sync.Once
}

func (c *captureReader) Read(p []byte) (int, error) {
	c.once.Do(func() {
		if c.onFirstByte != nil {
			c.onFirstByte()
		}
	})
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		c.buf.Write(p[:n])
	}
	return n, err
}

func addLog(p *models.RequestPayload, entry models.LogEntry) {
	p.Logs = append(p.Logs, entry)
}

func broadcastSummary(s *models.RequestSummary) {
	data, _ := json.Marshal(s)
	SSEHub.Broadcast("summary", string(data))
}

// 需要脱敏的 header key（大小写不敏感）
var sensitiveHeaders = map[string]bool{
	"authorization": true, "x-api-key": true, "api-key": true,
	"cookie": true, "set-cookie": true, "x-auth-token": true,
}

func sanitizeHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if sensitiveHeaders[strings.ToLower(k)] {
			if len(v) > 20 {
				out[k] = v[:15] + "***[REDACTED]"
			} else {
				out[k] = "***[REDACTED]"
			}
		} else {
			out[k] = v
		}
	}
	return out
}

func extractModel(body string) string {
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	if m, ok := parsed["model"].(string); ok {
		return m
	}
	return ""
}

func BroadcastLog(requestId, phase, level, source, message, details string) {
	entry := map[string]interface{}{
		"timestamp": time.Now().UnixMilli(),
		"requestId": requestId,
		"phase":     phase,
		"level":     level,
		"source":    source,
		"message":   message,
	}
	if details != "" {
		entry["details"] = details
	}
	data, _ := json.Marshal(entry)
	SSEHub.Broadcast("log", string(data))
}
