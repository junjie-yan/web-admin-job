package asynctask

import (
	"context"
	"fmt"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/coupon"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// importCoupon 批量导入优惠券（biz_module=coupon + type=import，upsert 语义）
//
// 定位键：商店域名 + 来源ID（两项均必填，库层联合唯一索引）。
// 定位命中：仅更新 Excel 中填写的字段，留空字段保持原值；未命中：新增记录。
// merchant_id 由商店域名查 coupon_merchant_base 自动关联（域名不存在时不阻断，仅不建立关联）。
// 批内同定位键多行保留最后一行。
type importCoupon struct {
	baseHandler
}

// newImportCoupon 创建导入处理器（由 importDispatcher 路由调用）
func newImportCoupon(svcCtx *svc.ServiceContext) *importCoupon {
	return &importCoupon{baseHandler{svcCtx: svcCtx}}
}

// process 处理已领取的导入任务（rows 为输入 Excel 数据行，已跳过表头）
func (h *importCoupon) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	logger := logx.WithContext(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	// 逐批解析 + upsert（每 50 行一批）
	nr := coupon.NewResolver(ctx, h.svcCtx.SitehubDB)
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

		// 1. 解析本批数据（解析时顺带按域名关联 merchant_id）
		var parsed []*coupon.Row
		var batchErrs []string
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			r, parseErr := coupon.ParseRow(j+2, rows[j], nr)
			if parseErr != nil {
				batchErrs = append(batchErrs, parseErr.Error())
				continue
			}
			if r != nil {
				if id, e := nr.ResolveMerchantID(r.MerchantDomain); e != nil {
					batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 查询商家失败 %v", r.RowNo, e))
					continue
				} else if id > 0 {
					r.MerchantID = &id
				}
				parsed = append(parsed, r)
			}
		}

		// 2. 按定位键（域名+来源ID）批量预查
		var keys []coupon.Key
		for _, r := range parsed {
			keys = append(keys, coupon.Key{MerchantDomain: r.MerchantDomain, SourceID: r.SourceID})
		}
		existIDs, err := coupon.FindIDsByKey(ctx, h.svcCtx.SitehubDB, keys)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 coupon 失败: %v", err))
			return
		}

		// 3. 分流：命中 → 逐条更新；未命中 → 批内按定位键去重（保留最后一条）后整批新增
		var updates []struct {
			id  uint64
			row *coupon.Row
		}
		pending := make(map[coupon.Key]*coupon.Row)
		var order []coupon.Key
		for _, r := range parsed {
			k := coupon.Key{MerchantDomain: r.MerchantDomain, SourceID: r.SourceID}
			if id, ok := existIDs[k]; ok {
				updates = append(updates, struct {
					id  uint64
					row *coupon.Row
				}{id: id, row: r})
				continue
			}
			if _, dup := pending[k]; !dup {
				order = append(order, k)
			}
			pending[k] = r
		}

		for _, u := range updates {
			if e := coupon.UpdateByID(ctx, h.svcCtx.SitehubDB, u.id, u.row); e != nil {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 更新失败 %v", u.row.RowNo, e))
				continue
			}
			updated++
		}

		final := make([]*coupon.Row, 0, len(order))
		for _, k := range order {
			final = append(final, pending[k])
		}
		if len(final) > 0 {
			if e := coupon.BatchInsert(ctx, h.svcCtx.SitehubDB, final); e != nil {
				// 整批失败降级逐条插入以定位错误行
				for _, r := range final {
					if e := coupon.BatchInsert(ctx, h.svcCtx.SitehubDB, []*coupon.Row{r}); e != nil {
						batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: %v", r.RowNo, e))
						continue
					}
					inserted++
				}
			} else {
				inserted += uint64(len(final))
			}
		}

		fail += uint64(len(batchErrs))
		errs = append(errs, batchErrs...)
		h.reportProgress(ctx, task.ID, total, inserted+updated, fail, calcProgress(uint64(end), total), "")
	}

	// 4. 写结果文件（失败明细）
	resultURL := ""
	if fail > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, asyncjob.TypeImport, errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}

	// 5. 终态
	summary := fmt.Sprintf("导入完成：成功 %d（新增 %d，更新 %d）", inserted+updated, inserted, updated)
	if fail == 0 {
		h.finishSuccessWithMsg(ctx, task.ID, total, inserted+updated, summary)
	} else {
		h.finishPartial(ctx, task.ID, total, inserted+updated, fail,
			fmt.Sprintf("%s，失败 %d", summary, fail), resultURL)
	}
}
