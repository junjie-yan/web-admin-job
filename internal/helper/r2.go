// Package helper 提供 web-admin-job 内部使用的工具函数（R2 文件下载/上传、Excel 解析/写入等）。
//
// 本包不依赖 web-admin 模块（避免循环依赖），独立封装 S3 兼容协议的文件读写与 excelize 操作。
package helper

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Config R2 连接配置（与 internal/config.R2Conf 字段一一对应）
type R2Config struct {
	AccessKey      string
	SecretKey      string
	Region         string
	Bucket         string
	Endpoint       string
	ForcePathStyle bool
	PublicBaseURL  string
}

// NewR2Client 创建 S3 兼容客户端（连接 Cloudflare R2）
func NewR2Client(ctx context.Context, c R2Config) (*s3.Client, error) {
	if c.AccessKey == "" || c.SecretKey == "" || c.Bucket == "" || c.Endpoint == "" {
		return nil, fmt.Errorf("r2: access_key/secret_key/bucket/endpoint are required")
	}
	region := c.Region
	if region == "" {
		region = "auto"
	}
	awsCfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(region),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("r2: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(c.Endpoint)
		o.UsePathStyle = c.ForcePathStyle
	})
	return client, nil
}

// DownloadFile 从 R2 下载指定 URL/Key 的文件内容
// urlOrKey 可传入完整 URL（带 PublicBaseURL）或对象 key
func DownloadFile(ctx context.Context, client *s3.Client, bucket, urlOrKey string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("r2: client is nil")
	}
	key := ExtractR2Key(urlOrKey)
	if key == "" {
		return nil, fmt.Errorf("r2: empty key after extract from %q", urlOrKey)
	}
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("r2: get object %q: %w", key, err)
	}
	defer out.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(out.Body); err != nil {
		return nil, fmt.Errorf("r2: read object body: %w", err)
	}
	return buf.Bytes(), nil
}

// UploadFile 上传字节流到 R2，返回写入后的资源 URL
// contentType 例如 "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
// kind 为业务分类（如 async_task_import、async_task_result），用于组织 key 前缀
func UploadFile(ctx context.Context, client *s3.Client, bucket, publicBaseURL, kind, fileName string, data []byte, contentType string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("r2: client is nil")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("r2: empty data")
	}
	ext := strings.ToLower(fileExt(fileName))
	key := buildAsyncObjectKey(kind, ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("r2: put object: %w", err)
	}
	return BuildR2URL(key, publicBaseURL), nil
}

// buildAsyncObjectKey 生成 async_task 专用 key: async/<kind>/<YYYYMMDD>/<rand><ext>
func buildAsyncObjectKey(kind, ext string) string {
	if kind == "" {
		kind = "common"
	}
	dateStr := time.Now().Format("20060102")
	name := randomName(16)
	return fmt.Sprintf("async/%s/%s/%s%s", kind, dateStr, name, ext)
}

// fileExt 从文件名提取扩展名（含 .），没有则返回空串
func fileExt(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx < 0 {
		return ""
	}
	return name[idx:]
}

// ExtractR2Key 从 URL 或 key 中提取 R2 对象 key（剥离域名/前导斜杠）
func ExtractR2Key(urlOrKey string) string {
	if urlOrKey == "" {
		return ""
	}
	s := urlOrKey
	if i := strings.Index(s, "://"); i != -1 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j != -1 {
			s = s[j+1:]
		} else {
			return ""
		}
	}
	s = strings.TrimPrefix(s, "/")
	return s
}

// BuildR2URL 根据 key 与 publicBaseURL 构造资源 URL
// publicBaseURL 为空时返回 "/key"
func BuildR2URL(key, publicBaseURL string) string {
	key = strings.TrimPrefix(key, "/")
	base := strings.TrimRight(publicBaseURL, "/")
	if base == "" {
		return "/" + key
	}
	return base + "/" + key
}
