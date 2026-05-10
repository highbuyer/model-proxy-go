package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"model-proxy-go/internal/db"
)

// ChatMLMessage 标准训练格式
type ChatMLMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatMLRecord struct {
	Messages []ChatMLMessage `json:"messages"`
}

func ExportHandler(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "success" // 默认只导出成功的
	}
	since := r.URL.Query().Get("since")
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "500"
	}

	payloads, err := db.GetPayloadsForExport(status, since, limit)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/x-jsonlines")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=training_%s.jsonl", time.Now().Format("20060102_150405")))

	enc := json.NewEncoder(w)
	exported := 0
	skipped := 0
	for _, p := range payloads {
		record := convertToChatML(p, r.URL.Query().Get("include_sys") == "1")
		if record == nil || len(record.Messages) == 0 {
			skipped++
			continue
		}
		if err := enc.Encode(record); err != nil {
			break
		}
		exported++
	}
	fmt.Printf("[Export] 导出 %d 条, 跳过 %d 条 (status=%s, since=%s)\n", exported, skipped, status, since)
}

// convertToChatML 将原始请求/响应转换为 ChatML 格式
func convertToChatML(p db.ExportPayload, includeSys bool) *ChatMLRecord {
	// 解析原始请求体
	var origReq struct {
		Model    string `json:"model"`
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(p.OrigBody), &origReq); err != nil {
		return nil
	}

	var messages []ChatMLMessage

	// 提取 system prompt — 默认不导出（训练时系统提示词含大量噪声）
	if includeSys && len(origReq.System) > 0 {
		var sysBlocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(origReq.System, &sysBlocks) == nil {
			var sysTexts []string
			for _, b := range sysBlocks {
				if b.Type == "text" && b.Text != "" {
					sysTexts = append(sysTexts, b.Text)
				}
			}
			if len(sysTexts) > 0 {
				messages = append(messages, ChatMLMessage{Role: "system", Content: strings.Join(sysTexts, "\n")})
			}
		}
	}

	// 提取用户/助手消息
	for _, msg := range origReq.Messages {
		text := extractTextContent(msg.Content)
		if text == "" {
			continue
		}
		role := msg.Role
		if role == "user" || role == "assistant" {
			messages = append(messages, ChatMLMessage{Role: role, Content: text})
		}
	}

	// 解析 SSE 响应，提取最终 assistant 回复
	if p.ResponseBody != "" {
		assistantText := parseSSEResponse(p.ResponseBody)
		if assistantText != "" {
			messages = append(messages, ChatMLMessage{Role: "assistant", Content: assistantText})
		}
	}

	if len(messages) == 0 {
		return nil
	}
	return &ChatMLRecord{Messages: messages}
}

// extractTextContent 从 Anthropic content 数组中提取纯文本
func extractTextContent(content json.RawMessage) string {
	// 尝试作为字符串
	var str string
	if json.Unmarshal(content, &str) == nil {
		return str
	}

	// 尝试作为 content block 数组
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// parseSSEResponse 解析 SSE 事件流，提取 assistant 文本
func parseSSEResponse(sseText string) string {
	var textParts []string
	scanner := bufio.NewScanner(strings.NewReader(sseText))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			textParts = append(textParts, event.Delta.Text)
		}
	}
	return strings.Join(textParts, "")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
