package asynctask

// sitemap XML 生成（纯函数，迁移自 web-datahub internal/service/sitemap/xml.go）。
// sitemap 不落本地文件，内存生成字节后直传 R2。

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// sitemapURL sitemap URL 条目
type sitemapURL struct {
	XMLName  xml.Name `xml:"url"`
	Loc      string   `xml:"loc"`
	LastMod  string   `xml:"lastmod,omitempty"`
	Change   string   `xml:"changefreq,omitempty"`
	Priority float64  `xml:"priority,omitempty"`
}

// sitemapIndex sitemap 索引文件
type sitemapIndex struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	XMLNS    string       `xml:"xmlns,attr"`
	Sitemaps []sitemapRef `xml:"sitemap"`
}

// sitemapRef sitemap 引用
type sitemapRef struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// sitemapDoc sitemap 文件
type sitemapDoc struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// normalizeSitemapURL 规范化 sitemap URL
//   - raw 空 → baseURL + fallbackPath
//   - raw 是完整 URL (含 scheme) → 替换为 baseURL + 原 path
//   - raw 是相对路径 → baseURL + raw
func normalizeSitemapURL(raw, baseURL, fallbackPath string) string {
	if raw == "" {
		return baseURL + fallbackPath
	}
	if strings.HasPrefix(raw, "http") {
		if idx := strings.Index(raw, "://"); idx != -1 {
			afterScheme := raw[idx+3:]
			if pathIdx := strings.Index(afterScheme, "/"); pathIdx != -1 {
				return baseURL + afterScheme[pathIdx:]
			}
			return baseURL
		}
	}
	return baseURL + raw
}

// marshalSitemap 序列化 sitemap urlset
func marshalSitemap(sm sitemapDoc) ([]byte, error) {
	data, err := xml.MarshalIndent(sm, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 sitemap 失败: %w", err)
	}
	return []byte(xml.Header + string(data)), nil
}

// marshalSitemapIndex 序列化 sitemap 索引
func marshalSitemapIndex(refs []sitemapRef) ([]byte, error) {
	idx := sitemapIndex{
		XMLNS:    "http://www.sitemaps.org/schemas/sitemap/0.9",
		Sitemaps: refs,
	}
	data, err := xml.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 sitemap index 失败: %w", err)
	}
	return []byte(xml.Header + string(data)), nil
}

// sanitizeSitemapDomain 校验站点域名（防路径穿越）：转小写，仅允许域名字符
func sanitizeSitemapDomain(domain string) (string, error) {
	domain = strings.ToLower(domain)
	if domain == "" {
		return "", fmt.Errorf("站点域名为空")
	}
	for i := 0; i < len(domain); i++ {
		c := domain[i]
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '.' || c == '-') {
			return "", fmt.Errorf("站点域名 %q 含非法字符", domain)
		}
	}
	return domain, nil
}
