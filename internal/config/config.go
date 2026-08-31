package config

import (
	"github.com/suyuan32/simple-admin-common/config"
	"github.com/suyuan32/simple-admin-common/plugins/mq/asynq"

	"github.com/zeromicro/go-zero/zrpc"
)

type Config struct {
	zrpc.RpcServerConf
	DatabaseConf config.DatabaseConf // web_admin 库（async_task 表）
	SitehubDBConf config.DatabaseConf // sitehub 库（app_detail 等业务表）
	RedisConf    config.RedisConf
	AsynqConf    asynq.AsynqConf
	TaskConf     TaskConf
	R2Conf       R2Conf
	CoreRpc      zrpc.RpcClientConf // 保留配置位，未来若拆分 coreclient 可启用
}

type TaskConf struct {
	EnableScheduledTask bool `json:",default=true"`
	EnableDPTask        bool `json:",default=true"`
}

// R2Conf Cloudflare R2 (S3 兼容) 配置，与 web-admin 的 R2Conf 共用同一 bucket
type R2Conf struct {
	AccessKey      string `json:",optional"`
	SecretKey      string `json:",optional"`
	Region         string `json:",optional"`
	Bucket         string `json:",optional"`
	Endpoint       string `json:",optional"`
	ForcePathStyle bool   `json:",optional"`
	// PublicBaseURL 公共访问域名（拼接最终 URL，不含末尾 /）
	PublicBaseURL string `json:",optional"`
}
