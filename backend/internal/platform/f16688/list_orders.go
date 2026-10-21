package f16688

import (
	"context"
	"time"

	"kamihub/internal/platform"
)

// ListOrders 拉取上游采购记录，供对账做订单级比对。
//
// 为什么单独一个文件：这个方法不属于 platform.Client 接口的必需能力
// （不是所有适配器都能列单），只在支持的上游上实现，
// 由对账服务用类型断言探测。
//
// 16688 的 /purchase/list 只支持 page_no / page_size / status / keywords，
// **没有时间区间参数**，所以只能分页拉取后在本地按 create_time 过滤。
// 为了不把上游拉爆，limit 由调用方控制（默认 500）。
func (c *Client) ListOrders(ctx context.Context, since time.Time, limit int) ([]platform.PurchaseResult, error) {
	if limit <= 0 {
		limit = 200
	}
	const pageSize = 100 // 官方上限

	sinceUnix := since.Unix()
	out := make([]platform.PurchaseResult, 0, limit)

	for pageNo := 1; len(out) < limit; pageNo++ {
		var data struct {
			Total int        `json:"total"`
			List  []apiOrder `json:"list"`
		}
		biz := map[string]any{
			"page_no":   pageNo,
			"page_size": pageSize,
		}
		if err := c.call(ctx, "/openApi/purchase/list", biz, &data); err != nil {
			// 已经拿到一部分时不要把整批结果丢掉：对账宁可少比几单，
			// 也不要因为翻页失败而完全跳过该平台。
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		if len(data.List) == 0 {
			break
		}

		for i := range data.List {
			o := data.List[i]
			// create_time 为 0 说明上游没给时间，保守保留（宁可多比对）
			if o.CreateTime > 0 && o.CreateTime < sinceUnix {
				continue
			}
			out = append(out, *o.toResult())
			if len(out) >= limit {
				break
			}
		}

		// 本页已经全部早于时间窗，后续页只会更早，可以停
		allBefore := true
		for i := range data.List {
			if data.List[i].CreateTime == 0 || data.List[i].CreateTime >= sinceUnix {
				allBefore = false
				break
			}
		}
		if allBefore {
			break
		}

		// 已拉完所有记录
		if pageNo*pageSize >= data.Total {
			break
		}
	}

	return out, nil
}

// 编译期断言：确保 f16688.Client 满足「可列单」的结构要求。
// 注意这里不能直接引用 service 包里的未导出接口（会形成反向依赖），
// 因此用本地同签名接口做断言，效果等价。
var _ interface {
	ListOrders(ctx context.Context, since time.Time, limit int) ([]platform.PurchaseResult, error)
} = (*Client)(nil)
