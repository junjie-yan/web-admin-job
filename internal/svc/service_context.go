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
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/ent"
	"github.com/junjie-yan/web-admin-job/internal/config"
	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/types/periodicconfig"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

type ServiceContext struct {
	Config       config.Config
	DB           *ent.Client // web_admin 库 ent 客户端（任务/日志表）
	SitehubDB    *sql.DB     // sitehub 库原生 SQL 连接（app_detail 批量操作）
	AsynqServer  *asynq.Server
	AsynqPTM     *asynq.PeriodicTaskManager
	AsyncTaskMgr *asyncjob.Manager          // async_task 表管理器
	R2           map[string]*helper.Instance // R2 实例列表（按桶名取用；未配置时为空 map）
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

	// 初始化 R2 实例列表（未配置时为空 map，相关功能禁用；Bucket 重复/必填缺失启动报错）
	r2Cfgs := make([]helper.R2Config, 0, len(c.R2Confs))
	for _, rc := range c.R2Confs {
		r2Cfgs = append(r2Cfgs, helper.R2Config{
			AccessKey:      rc.AccessKey,
			SecretKey:      rc.SecretKey,
			Region:         rc.Region,
			Bucket:         rc.Bucket,
			Endpoint:       rc.Endpoint,
			ForcePathStyle: rc.ForcePathStyle,
			PublicBaseURL:  rc.PublicBaseURL,
		})
	}
	r2Instances, err := helper.NewInstances(context.Background(), r2Cfgs)
	logx.Must(err)

	return &ServiceContext{
		Config:       c,
		DB:           db,
		SitehubDB:    sitehubDB,
		AsynqServer:  c.AsynqConf.WithOriginalRedisConf(c.RedisConf).NewServer(),
		AsynqPTM:     c.AsynqConf.NewPeriodicTaskManager(periodicconfig.NewEntConfigProvider(db)),
		AsyncTaskMgr: asyncTaskMgr,
		R2:           r2Instances,
	}
}

// R2ByKey 按桶名取用 R2 实例（job 使用 "sem-web-private" 私有桶）
func (svcCtx *ServiceContext) R2ByKey(bucket string) (*helper.Instance, error) {
	if inst, ok := svcCtx.R2[bucket]; ok && inst != nil {
		return inst, nil
	}
	return nil, fmt.Errorf("r2: instance %q not configured", bucket)
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
