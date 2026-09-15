// Package slugify 提供通用的 SEO 友好 URL slug 生成方法，供各业务域公共调用。
//
// 注意：本包在 web-admin 与 web-admin-job 两个仓库各存一份（与 r2 helper 同惯例），
// 修改时需同步两份实现。
package slugify

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	// sepRe 小写字母/数字以外的字符（空格、标点、符号、下划线）折叠为分隔符
	sepRe = regexp.MustCompile(`[^a-z0-9]+`)
	// edgeRe 去除首尾连字符
	edgeRe = regexp.MustCompile(`^-+|-+$`)
)

// Slugify 将任意文本转为 SEO 友好的 URL slug（kebab-case），如 "Sketchbook AA" → "sketchbook-aa"
//
// 遵循 URL slug 通用规范：
//  1. NFKD 归一化并去除组合变音符（café → cafe）
//  2. 全小写（URL 路径区分大小写，混合大小写会产生重复内容）
//  3. 非小写字母/数字折叠为单个连字符（Google 把连字符当分词符，下划线当连接符，故不用下划线）
//  4. 无首尾连字符、无连续连字符
//
// 纯 CJK/符号输入折叠后为空串，由调用方决定兜底（见 SlugifyHash）
func Slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	s = strings.ToLower(b.String())
	s = sepRe.ReplaceAllString(s, "-")
	return edgeRe.ReplaceAllString(s, "")
}

// SlugifyHash 同 Slugify，但非空输入折叠为空时（纯 CJK/符号）回退为输入内容的
// FNV-1a 32 位十六进制哈希（确定性：同名输入始终生成同一值，利于查重与幂等），如 "微信" → "e11a9c1d"。
// 空串输入返回空串。
func SlugifyHash(s string) string {
	if s == "" {
		return ""
	}
	slug := Slugify(s)
	if slug != "" {
		return slug
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum32())
}

// Path 将多个文本段转为 URL 路径，每段先做 SlugifyHash 归一化，空段自动跳过。
// 如 Path("app", "Sketchbook AA") → "/app/sketchbook-aa"。
// 末段是记录的标识段，归一化后为空（输入为空串）时返回空串，由调用方决定兜底。
func Path(segments ...string) string {
	if len(segments) == 0 || SlugifyHash(segments[len(segments)-1]) == "" {
		return ""
	}
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		if s := SlugifyHash(seg); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "/" + strings.Join(parts, "/")
}
