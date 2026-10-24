// Package cacheflush 提供对 C 端站点 API（web-api-go）缓存刷新接口的极简 HTTP 客户端。
//
// job 仓库不能反向依赖 web-admin，故独立实现；
// 请求/响应结构与 web-admin/pkg/webapi.RefreshCacheRequest 保持一致，
// 仅使用 job 侧需要的 site/site_id/page 三个字段（整类前缀刷新，不带精确 ID）。
package cacheflush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Config 前端 API 服务配置
type Config struct {
	// BaseURL 服务地址，如 http://127.0.0.1:3210
	BaseURL string `json:",default=http://127.0.0.1:3210"`
	// RefreshToken 缓存刷新接口共享令牌（X-Refresh-Token 请求头），与 web-api-go
	// security.cache_refresh_token 同值；未启用令牌时留空
	RefreshToken string `json:",optional"`
}

// RefreshRequest 缓存刷新请求（范围逐级收窄；job 侧只用整类模式：site+site_id+page）
type RefreshRequest struct {
	Site   string `json:"site,omitempty"`   // 站点模块名：coupon/app/site/blog
	SiteID uint64 `json:"site_id,omitempty"` // 站点实例 ID，0 表示跨实例整类
	Page   string `json:"page,omitempty"`    // 页面/资源类型（缓存 key 第一段，须搭配 site）
}

// refreshResponse 对应 web-api-go 的 api.Response
type refreshResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Deleted int64 `json:"deleted"`
	} `json:"data"`
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// Refresh 调用前端 API 的缓存刷新接口，返回删除的键数量
func Refresh(ctx context.Context, cfg Config, req RefreshRequest) (int64, error) {
	if cfg.BaseURL == "" {
		return 0, fmt.Errorf("cacheflush: 前端 API 地址未配置")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("cacheflush: 序列化请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.BaseURL+"/api/v1/site/cache/refresh", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.RefreshToken != "" {
		httpReq.Header.Set("X-Refresh-Token", cfg.RefreshToken)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("cacheflush: 调用前端 API 失败: %w", err)
	}
	defer resp.Body.Close()

	var result refreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("cacheflush: 解析前端 API 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK || result.Code != 0 {
		return 0, fmt.Errorf("cacheflush: 前端 API 返回错误 http=%d code=%d msg=%s",
			resp.StatusCode, result.Code, result.Message)
	}
	return result.Data.Deleted, nil
}
