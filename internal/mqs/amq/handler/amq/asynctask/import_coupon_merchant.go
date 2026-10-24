package asynctask

import (
	"context"
	"fmt"
	"strings"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/merchant"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// importCouponMerchantAdd 批量新增优惠券商家（biz_module=coupon_merchant_add + type=import）
//
// 列与现有商家模板一致（商店域名为定位键）；域名已存在则该行失败，商店名称必填。
type importCouponMerchantAdd struct {
	baseHandler
}

func newImportCouponMerchantAdd(svcCtx *svc.ServiceContext) *importCouponMerchantAdd {
	return &importCouponMerchantAdd{baseHandler{svcCtx: svcCtx}}
}

func (h *importCouponMerchantAdd) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	runCouponMerchant(ctx, h.baseHandler, task, rows, true)
}

// importCouponMerchantUpdate 批量更新优惠券商家（biz_module=coupon_merchant_update + type=import）
//
// 商店域名为定位键（必须已存在），仅更新填写的字段；域名不存在则该行失败。
type importCouponMerchantUpdate struct {
	baseHandler
}

func newImportCouponMerchantUpdate(svcCtx *svc.ServiceContext) *importCouponMerchantUpdate {
	return &importCouponMerchantUpdate{baseHandler{svcCtx: svcCtx}}
}

func (h *importCouponMerchantUpdate) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	runCouponMerchant(ctx, h.baseHandler, task, rows, false)
}

// runCouponMerchant 商家新增/更新共用编排：解析 → 批内去重 → 判重/存在校验 → 落库 → 结果文件。
// 行解析复用 merchant.ParseRow（与既有 upsert 导入同列序），仅判重/存在校验语义随 add/update 切换。
func runCouponMerchant(ctx context.Context, h baseHandler, task *asyncjob.Task, rows [][]string, isAdd bool) {
	logger := logx.WithContext(ctx)
	// 聚合本任务成功写入的缓存范围，结束时统一刷新 C 端缓存（失败仅记日志）
	// 本 handler 与 import_merchant_base 同操作 coupon_merchant_base 表（全局 base 表），整类跨实例刷新
	fl := newCacheFlusher(h.svcCtx)
	defer fl.flush(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	nr := merchant.NewResolver(ctx, h.svcCtx.SitehubDB)
	var (
		okCnt    uint64
		updated  uint64
		failCnt  uint64
		inserted uint64
		errs     []string
	)

	const batchSize = 50
	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批
		var parsed []*merchant.Row
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue
			}
			r, perr := merchant.ParseRow(j+2, rows[j], nr)
			if perr != nil {
				failCnt++
				errs = append(errs, perr.Error())
				continue
			}
			if r == nil {
				continue
			}
			// add 语义：商店名称必填
			if isAdd && r.Name == nil {
				failCnt++
				errs = append(errs, fmt.Sprintf("第 %d 行: 商店名称 必填", r.RowNo))
				continue
			}
			parsed = append(parsed, r)
		}

		// 2. 批内按域名去重（保留最后一行，与既有 upsert 行为一致）
		byDomain := make(map[string]*merchant.Row, len(parsed))
		for _, r := range parsed {
			byDomain[strings.ToLower(r.Domain)] = r
		}

		// 3. 判重/存在校验 + 落库
		for _, r := range byDomain {
			existingID, derr := merchant.FindIDsByDomain(ctx, h.svcCtx.SitehubDB, []string{r.Domain})
			if derr != nil {
				failCnt++
				errs = append(errs, fmt.Sprintf("第 %d 行: 查询失败 %v", r.RowNo, derr))
				continue
			}
			exists := false
			var id uint64
			if v, ok := existingID[strings.ToLower(r.Domain)]; ok {
				exists = true
				id = v
			}

			switch {
			case isAdd && exists:
				failCnt++
				errs = append(errs, fmt.Sprintf("第 %d 行(%s): 商店域名已存在（ID=%d）", r.RowNo, r.Domain, id))
				continue
			case !isAdd && !exists:
				failCnt++
				errs = append(errs, fmt.Sprintf("第 %d 行(%s): 商店域名不存在", r.RowNo, r.Domain))
				continue
			case exists:
				if err := merchant.UpdateByID(ctx, h.svcCtx.SitehubDB, id, r); err != nil {
					failCnt++
					errs = append(errs, fmt.Sprintf("第 %d 行: 更新失败 %v", r.RowNo, err))
					continue
				}
				updated++
				fl.add(siteCoupon, 0, pageMerchants, pageStore)
			default:
				if err := merchant.BatchInsert(ctx, h.svcCtx.SitehubDB, []*merchant.Row{r}); err != nil {
					failCnt++
					errs = append(errs, fmt.Sprintf("第 %d 行: 新增失败 %v", r.RowNo, err))
					continue
				}
				inserted++
				fl.add(siteCoupon, 0, pageMerchants, pageStore)
			}
			okCnt++
		}

		logx.WithContext(ctx).Infof("coupon merchant batch done: rows=%d", len(parsed))
	}

	// 结果文件
	resultURL := ""
	if len(errs) > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, asyncjob.TypeImport, errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}
	summary := fmt.Sprintf("完成：成功 %d（新增 %d，更新 %d），失败 %d", okCnt, inserted, updated, failCnt)
	if failCnt == 0 {
		h.finishSuccessWithMsg(ctx, task.ID, total, okCnt, summary)
		return
	}
	h.finishPartial(ctx, task.ID, total, okCnt, failCnt, summary, resultURL)
}
