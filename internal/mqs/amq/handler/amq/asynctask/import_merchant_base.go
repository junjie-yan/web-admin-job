package asynctask

import (
	"context"
	"fmt"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/merchant"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// importMerchantBase 批量导入优惠券商家（biz_module=coupon_merchant_base + type=import，upsert 语义）
//
// 定位键：商店域名（库层 UNIQUE(domain)），必填。
// 定位命中：仅更新 Excel 中填写的字段，留空字段保持原值；未命中：新增记录（商店名称必填）。
// 批内同域名多行保留最后一行。
type importMerchantBase struct {
	baseHandler
}

// newImportMerchantBase 创建导入处理器（由 importDispatcher 路由调用）
func newImportMerchantBase(svcCtx *svc.ServiceContext) *importMerchantBase {
	return &importMerchantBase{baseHandler{svcCtx: svcCtx}}
}

// process 处理已领取的导入任务（rows 为输入 Excel 数据行，已跳过表头）
func (h *importMerchantBase) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	logger := logx.WithContext(ctx)
	// 聚合本任务成功写入的缓存范围，结束时统一刷新 C 端缓存（失败仅记日志）
	// coupon_merchant_base 为全局 base 表，整类跨实例刷新（SiteID=0）
	fl := newCacheFlusher(h.svcCtx)
	defer fl.flush(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	// 逐批解析 + upsert（每 50 行一批）
	nr := merchant.NewResolver(ctx, h.svcCtx.SitehubDB)
	const batchSize = 50
	var (
		inserted uint64
		updated  uint64
		fail     uint64
		errs     []string // 错误明细（写入结果文件时最多保留 200 条）
	)

	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批数据
		var parsed []*merchant.Row
		var batchErrs []string
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			r, parseErr := merchant.ParseRow(j+2, rows[j], nr)
			if parseErr != nil {
				batchErrs = append(batchErrs, parseErr.Error())
				continue
			}
			if r != nil {
				parsed = append(parsed, r)
			}
		}

		// 2. 按定位键（域名）批量预查
		var domains []string
		for _, r := range parsed {
			domains = append(domains, r.Domain)
		}
		existIDs, err := merchant.FindIDsByDomain(ctx, h.svcCtx.SitehubDB, domains)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 coupon_merchant_base 失败: %v", err))
			return
		}

		// 3. 分流：命中 → 逐条更新；未命中 → 批内按域名去重（保留最后一条）后整批新增
		var updates []struct {
			id  uint64
			row *merchant.Row
		}
		pending := make(map[string]*merchant.Row)
		var order []string
		for _, r := range parsed {
			if id, ok := existIDs[r.Domain]; ok {
				updates = append(updates, struct {
					id  uint64
					row *merchant.Row
				}{id: id, row: r})
				continue
			}
			if _, dup := pending[r.Domain]; !dup {
				order = append(order, r.Domain)
			}
			pending[r.Domain] = r
		}

		for _, u := range updates {
			if e := merchant.UpdateByID(ctx, h.svcCtx.SitehubDB, u.id, u.row); e != nil {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 更新失败 %v", u.row.RowNo, e))
				continue
			}
			updated++
			fl.add(siteCoupon, 0, pageMerchants, pageStore)
		}

		// 4. 新增：名称必填（新增路径校验，更新路径留空保持原值）
		final := make([]*merchant.Row, 0, len(order))
		for _, domain := range order {
			r := pending[domain]
			if r.Name == nil {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 新增商家 %s 时商店名称必填", r.RowNo, r.Domain))
				continue
			}
			final = append(final, r)
		}
		if len(final) > 0 {
			if e := merchant.BatchInsert(ctx, h.svcCtx.SitehubDB, final); e != nil {
				// 整批失败降级逐条插入以定位错误行
				for _, r := range final {
					if e := merchant.BatchInsert(ctx, h.svcCtx.SitehubDB, []*merchant.Row{r}); e != nil {
						batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: %v", r.RowNo, e))
						continue
					}
					inserted++
					fl.add(siteCoupon, 0, pageMerchants, pageStore)
				}
			} else {
				inserted += uint64(len(final))
				fl.add(siteCoupon, 0, pageMerchants, pageStore)
			}
		}

		fail += uint64(len(batchErrs))
		errs = append(errs, batchErrs...)
		h.reportProgress(ctx, task.ID, total, inserted+updated, fail, calcProgress(uint64(end), total), "")
	}

	// 5. 写结果文件（失败明细）
	resultURL := ""
	if fail > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, asyncjob.TypeImport, errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}

	// 6. 终态
	summary := fmt.Sprintf("导入完成：成功 %d（新增 %d，更新 %d）", inserted+updated, inserted, updated)
	if fail == 0 {
		h.finishSuccessWithMsg(ctx, task.ID, total, inserted+updated, summary)
	} else {
		h.finishPartial(ctx, task.ID, total, inserted+updated, fail,
			fmt.Sprintf("%s，失败 %d", summary, fail), resultURL)
	}
}
