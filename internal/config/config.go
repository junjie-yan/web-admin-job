package config

import (
	"github.com/suyuan32/simple-admin-common/config"
	"github.com/suyuan32/simple-admin-common/plugins/mq/asynq"

	"github.com/zeromicro/go-zero/zrpc"
)

type Config struct {
	zrpc.RpcServerConf
	DatabaseConf  config.DatabaseConf // web_admin 库（async_task 表）
	SitehubDBConf config.DatabaseConf // sitehub 库（app_detail 等业务表）
	RedisConf     config.RedisConf
	AsynqConf     asynq.AsynqConf
	TaskConf      TaskConf
	R2Confs       []R2Conf
	WebAPIConf    WebAPIConf // C 端站点 API（web-api-go），导入成功后调用其缓存刷新接口
}

// WebAPIConf 前端站点 API 服务配置（与 web-admin 的 webapi.Config 对应；job 不反向依赖 web-admin，独立声明）
type WebAPIConf struct {
	// BaseURL 服务地址，如 http://127.0.0.1:3210
	BaseURL string `json:",default=http://127.0.0.1:3210"`
	// RefreshToken 缓存刷新接口共享令牌，与 web-api-go security.cache_refresh_token 同值；未启用时留空
	RefreshToken string `json:",optional"`
}

type TaskConf struct {
	EnableDPTask bool `json:",default=true"`
}

// R2Conf Cloudflare R2 (S3 兼容) 配置，R2Confs 列表中的一条实例（与 web-admin 的 r2.Config 对应）
// 实例标识即 Bucket 名，业务代码按桶名取用
type R2Conf struct {
	AccessKey      string `json:",optional"`
	SecretKey      string `json:",optional"`
	Region         string `json:",optional"`
	Bucket         string `json:",optional"`
	Endpoint       string `json:",optional"`
	ForcePathStyle bool   `json:",optional"`
	// PublicBaseURL 公共访问域名（拼接最终 URL，不含末尾 /）；为空时返回 /<key>
	PublicBaseURL string `json:",optional"`
}
