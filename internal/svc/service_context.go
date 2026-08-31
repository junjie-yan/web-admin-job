// Copyright 2023 The Ryan SU Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package svc

import (
	"context"
	"database/sql"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/ent"
	"github.com/junjie-yan/web-admin-job/internal/config"
	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/types/periodicconfig"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

type ServiceContext struct {
	Config          config.Config
	DB              *ent.Client // web_admin 库 ent 客户端（任务/日志表）
	SitehubDB       *sql.DB     // sitehub 库原生 SQL 连接（app_detail 批量操作）
	Redis           redis.UniversalClient
	AsynqServer     *asynq.Server
	AsynqScheduler  *asynq.Scheduler
	AsynqPTM        *asynq.PeriodicTaskManager
	AsyncTaskMgr    *asyncjob.Manager // async_task 表管理器
	R2Client        *s3.Client        // Cloudflare R2 客户端（可能为 nil：未配置时禁用文件下载/上传）
	R2Bucket        string
	R2PublicBaseURL string
}

func NewServiceContext(c config.Config) *ServiceContext {
	entOpts := []ent.Option{
		ent.Log(logx.Info),
		ent.Driver(c.DatabaseConf.NewNoCacheDriver()),
	}

	if c.DatabaseConf.Debug {
		entOpts = append(entOpts, ent.Debug())
	}

	db := ent.NewClient(entOpts...)

	// 初始化 sitehub 库原生 SQL 连接（app_detail 批量写入用）
	sitehubDB, err := sql.Open(c.SitehubDBConf.Type, c.SitehubDBConf.GetDSN())
	logx.Must(err)
	sitehubDB.SetMaxOpenConns(c.SitehubDBConf.MaxOpenConn)
	sitehubDB.SetMaxIdleConns(20)

	// 初始化 async_task 管理器（与 web-admin API 共享同一张 web_admin.async_task 表）
	asyncTaskMgr := asyncjob.NewManager(sitehubAdminDB(&c))
	if err := asyncTaskMgr.EnsureTable(context.Background()); err != nil {
		// async_task 表创建失败不阻塞启动（web-admin API 侧也会创建），仅记录日志
		logx.Errorf("asyncjob: ensure table failed: %v", err)
	}

	// 初始化 R2 客户端（可选，未配置时为 nil）
	var r2Client *s3.Client
	if c.R2Conf.AccessKey != "" && c.R2Conf.Bucket != "" {
		client, err := helper.NewR2Client(context.Background(), helper.R2Config{
			AccessKey:      c.R2Conf.AccessKey,
			SecretKey:      c.R2Conf.SecretKey,
			Region:         c.R2Conf.Region,
			Bucket:         c.R2Conf.Bucket,
			Endpoint:       c.R2Conf.Endpoint,
			ForcePathStyle: c.R2Conf.ForcePathStyle,
			PublicBaseURL:  c.R2Conf.PublicBaseURL,
		})
		if err != nil {
			logx.Errorf("init r2 client failed: %v, file operations will be disabled", err)
		} else {
			r2Client = client
		}
	}

	return &ServiceContext{
		Config:          c,
		DB:              db,
		SitehubDB:       sitehubDB,
		AsynqServer:     c.AsynqConf.WithOriginalRedisConf(c.RedisConf).NewServer(),
		AsynqScheduler:  c.AsynqConf.NewScheduler(),
		AsynqPTM:        c.AsynqConf.NewPeriodicTaskManager(periodicconfig.NewEntConfigProvider(db)),
		Redis:           c.RedisConf.MustNewUniversalRedis(),
		AsyncTaskMgr:    asyncTaskMgr,
		R2Client:        r2Client,
		R2Bucket:        c.R2Conf.Bucket,
		R2PublicBaseURL: c.R2Conf.PublicBaseURL,
	}
}

// sitehubAdminDB 返回连接到 web_admin 库的 *sql.DB，用于 async_task 表读写
// 注意：async_task 表在 web_admin 库，不在 sitehub 库；这里用 web_admin 库连接
func sitehubAdminDB(c *config.Config) *sql.DB {
	d, err := sql.Open(c.DatabaseConf.Type, c.DatabaseConf.GetDSN())
	logx.Must(err)
	d.SetMaxOpenConns(c.DatabaseConf.MaxOpenConn)
	d.SetMaxIdleConns(20)
	return d
}
