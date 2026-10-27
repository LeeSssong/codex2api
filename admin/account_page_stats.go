package admin

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

type accountPageStatsItem struct {
	Quota5h       *AccountQuotaWindow `json:"quota_5h"`
	Quota7d       *AccountQuotaWindow `json:"quota_7d"`
	Usage5hDetail *accountUsageWindow `json:"usage_5h_detail,omitempty"`
	Usage7dDetail *accountUsageWindow `json:"usage_7d_detail,omitempty"`
	// UsageTodayDetail 是"今日"(服务器时区当天 0 点起)的网关侧聚合。
	// 与 5h/7d 不同,这个字段对每个请求到的账号都必下发:零值代表
	// "今天没跑过",缺字段才代表"还没加载",前端据此区分 0 和占位。
	UsageTodayDetail *accountUsageWindow `json:"usage_today_detail,omitempty"`
	Billed5h         *float64            `json:"billed_5h,omitempty"`
	Billed7d         *float64            `json:"billed_7d,omitempty"`
	// OfficialUSD 是官方结算口径的累计成本（本地快照全窗口，默认一年）。
	// 与上面按本地日志算的 Billed7d 是两套账：网关只看得到自己转发的请求，
	// 官方账单还含用户直接用官方客户端的消耗。读的是快照表，不打上游。
	OfficialUSD *float64 `json:"official_usd,omitempty"`
	// OfficialUSD7d 是 OfficialUSD 的兼容别名。列表徽章已改为「官方结算」，
	// 不再只展示 7 天；旧前端仍读这个字段。
	OfficialUSD7d *float64 `json:"official_usd_7d,omitempty"`
	// OfficialUsageSynced 表示官方快照已成功同步过、但上游窗口内没有数据
	//（官方统计有滞后，或账号没有官方客户端消耗）。前端据此显示静态占位
	// 并停止重拉，而不是把「暂无数据」当成「还在加载」无限转圈。
	OfficialUsageSynced bool `json:"official_usage_synced,omitempty"`
}

// GetAccountPageStats loads log-derived usage and billing values after the
// visible account page has rendered. The request is strictly bounded to one
// page, avoiding any pool-wide aggregation on the first-screen path.
func (h *Handler) GetAccountPageStats(c *gin.Context) {
	ids, err := parseAccountListIDs(c.Query("ids"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "ids 参数无效")
		return
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"stats": map[int64]accountPageStatsItem{}})
		return
	}
	if len(ids) > accountListPageMax {
		writeError(c, http.StatusBadRequest, "ids 最多允许 500 个")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	now := time.Now()
	shortSnapshots, longSnapshots := h.accountQuotaSnapshots(ids)
	usage5h, shortErr := h.db.GetAccountQuotaWindowUsage(ctx, quotaWindowStarts(shortSnapshots, now), now)
	usage7d, longErr := h.db.GetAccountQuotaWindowUsage(ctx, quotaWindowStarts(longSnapshots, now), now)
	if shortErr != nil {
		log.Printf("获取当前页账号 5h 用量失败: %v", shortErr)
	}
	if longErr != nil {
		log.Printf("获取当前页账号长窗口用量失败: %v", longErr)
	}

	// 今日口径与全局统计一致:服务器时区(TZ 配置,默认宿主时区)当天 0 点起。
	// 失败时整列不下发,让前端保持占位,避免把"查询失败"显示成 0。
	todaySince := database.StartOfDay(now)
	usageToday, todayErr := h.db.GetAccountUsageSinceByIDs(ctx, ids, todaySince)
	if todayErr != nil {
		log.Printf("获取当前页账号今日用量失败: %v", todayErr)
	}
	todayModels, todayModelErr := h.db.GetAccountModelCountsSinceByIDs(ctx, ids, todaySince)
	if todayModelErr != nil {
		log.Printf("获取当前页账号今日模型分布失败: %v", todayModelErr)
		todayModels = map[int64]map[string]database.AccountModelCount{}
	}

	// 官方结算快照缺失只是少一行展示，不能拖垮整页统计。
	officialTotals, err := h.db.SumAccountDailyUsage(ctx, ids, whamDailyUsageKeepDays)
	if err != nil {
		log.Printf("获取当前页账号官方结算成本失败: %v", err)
		officialTotals = map[int64]database.AccountDailyUsageTotal{}
	}

	stats := make(map[int64]accountPageStatsItem, len(ids))
	missingOfficial := make([]int64, 0)
	for _, id := range ids {
		short, long := shortSnapshots[id], longSnapshots[id]
		quota5h := projectAccountQuotaWindow(short, now, usage5h[id], shortErr)
		quota7d := projectAccountQuotaWindow(long, now, usage7d[id], longErr)
		item := accountPageStatsItem{Quota5h: &quota5h, Quota7d: &quota7d}
		if value := usage5h[id]; value != nil && !short.WindowChanged && finite(value.AccountBilled) && finite(value.UserBilled) && value.AccountBilled >= 0 && value.UserBilled >= 0 {
			item.Usage5hDetail = &accountUsageWindow{
				Requests: value.Requests, Tokens: value.Tokens,
				AccountBilled: value.AccountBilled, UserBilled: value.UserBilled,
			}
		}
		if value := usage7d[id]; value != nil && !long.WindowChanged && finite(value.AccountBilled) && finite(value.UserBilled) && value.AccountBilled >= 0 && value.UserBilled >= 0 {
			item.Usage7dDetail = &accountUsageWindow{
				Requests: value.Requests, Tokens: value.Tokens,
				AccountBilled: value.AccountBilled, UserBilled: value.UserBilled,
			}
		}
		if todayErr == nil {
			todayWindow := &accountUsageWindow{}
			if value := usageToday[id]; value != nil {
				todayWindow.Requests = value.Requests
				todayWindow.Tokens = value.Tokens
				todayWindow.AccountBilled = value.AccountBilled
				todayWindow.UserBilled = value.UserBilled
			}
			if models := todayModels[id]; len(models) > 0 {
				todayWindow.ModelCounts = make(map[string]int64, len(models))
				todayWindow.ModelSuccessCounts = make(map[string]int64, len(models))
				todayWindow.ModelAvgFirstTokenMs = make(map[string]float64, len(models))
				for model, count := range models {
					todayWindow.ModelCounts[model] = count.Requests
					todayWindow.ModelSuccessCounts[model] = count.Success
					if count.AvgFirstTokenMs > 0 {
						todayWindow.ModelAvgFirstTokenMs[model] = count.AvgFirstTokenMs
					}
				}
				if len(todayWindow.ModelAvgFirstTokenMs) == 0 {
					todayWindow.ModelAvgFirstTokenMs = nil
				}
			}
			if finite(todayWindow.AccountBilled) && finite(todayWindow.UserBilled) {
				item.UsageTodayDetail = todayWindow
			}
		}
		if item.Usage5hDetail != nil {
			item.Billed5h = &item.Usage5hDetail.AccountBilled
		}
		if item.Usage7dDetail != nil {
			item.Billed7d = &item.Usage7dDetail.AccountBilled
		}
		// 没有快照的账号不下发这个字段，前端显示占位而不是 $0；
		// 当前页缺快照时异步回补，不挡本次响应。已经成功同步过但
		// 上游没有数据的账号下发显式空态，不再反复回补。
		if total, ok := officialTotals[id]; ok {
			usd := total.Credits / proxy.WhamCreditsPerUSD
			item.OfficialUSD = &usd
			item.OfficialUSD7d = &usd
		} else if h.whamDailySyncedOnceFor(id) {
			item.OfficialUsageSynced = true
		} else if h.store != nil && whamDailyUsageBackfillEligible(h.store.FindByID(id)) {
			missingOfficial = append(missingOfficial, id)
		}
		stats[id] = item
	}
	h.enqueueWhamDailyUsageBackfill(missingOfficial)
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}
