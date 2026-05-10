package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"model-proxy-go/internal/db"
	"model-proxy-go/internal/models"
)

// ========== 代理配置 API ==========

func ConfigList(w http.ResponseWriter, r *http.Request) {
	list, _ := db.ConfigList()
	if list == nil {
		list = []models.ProxyConfig{}
	}
	writeJSON(w, list)
}

func ConfigAdd(w http.ResponseWriter, r *http.Request) {
	var c models.ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	if err := db.ConfigAdd(&c); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, c)
}

func ConfigUpdate(w http.ResponseWriter, r *http.Request) {
	var c models.ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	if err := db.ConfigUpdate(&c); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"ok": "true"})
}

func ConfigDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/config/delete/"), 10, 64)
	db.ConfigDelete(id)
	writeJSON(w, map[string]string{"ok": "true"})
}

// ========== 请求日志 API ==========

func LogList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	keyword := r.URL.Query().Get("keyword")
	status := r.URL.Query().Get("status")

	summaries, total, _ := db.GetSummaries(limit, offset, keyword, status)
	writeJSON(w, models.PageResult{Data: summaries, Total: total})
}

func LogPayload(w http.ResponseWriter, r *http.Request) {
	requestId := strings.TrimPrefix(r.URL.Path, "/api/log/payload/")
	if requestId == "" {
		http.NotFound(w, r)
		return
	}
	payload, err := db.GetPayload(requestId)
	if err != nil || payload == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, payload)
}

func LogDelete(w http.ResponseWriter, r *http.Request) {
	requestId := strings.TrimPrefix(r.URL.Path, "/api/log/delete/")
	db.DeleteRequest(requestId)
	writeJSON(w, map[string]string{"ok": "true"})
}

// ========== 统计 API ==========

func Stats(w http.ResponseWriter, r *http.Request) {
	stats, _ := db.GetStats()
	writeJSON(w, stats)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
