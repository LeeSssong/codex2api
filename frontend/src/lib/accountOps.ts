// Source behavior: Sub2API 2.8.11 AccountQualityView / AccountOpsView (LGPL-3.0).
export const CANDY_PROMPT = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4`;
export interface QualityJudge {
  group_id: number;
  model_id: string;
  prompt: string;
}
export interface AccountQualityPlan {
  id: number;
  account_id: number;
  enabled: boolean;
  model: string;
  prompt: string;
  reasoning_effort: string;
  cron: string;
  samples: number;
  max_results: number;
  expected_answer: string;
  action: "remove_groups" | "disable_scheduling";
  remove_group_ids: number[];
  auto_restore: boolean;
  judge: QualityJudge | null;
  version: number;
  next_run?: string;
}
export interface AccountQualitySample {
  output: string;
  error?: string;
  verdict: string;
  reason: string;
  account_id?: number;
  group_id?: number;
  model_id?: string;
  duration_ms: number;
}
export interface AccountQualityRound {
  id: number;
  plan_id: number;
  account_id: number;
  account_name: string;
  started_at: string;
  completed_at: string;
  outcome: string;
  action: string;
  passed_count: number;
  total_count: number;
  plan: AccountQualityPlan;
  results?: AccountQualitySample[];
}
export interface AccountOpsConfig {
  enabled: boolean;
  recipient: string;
  balance_low: boolean;
  weekly_quota: boolean;
  cooldown_minutes: number;
}
export interface SMTPConfig {
  host: string;
  port: number;
  username: string;
  password?: string;
  password_configured: boolean;
  from: string;
  tls_mode: "starttls" | "tls";
}
export interface AccountOpsSettings {
  config: AccountOpsConfig;
  smtp: SMTPConfig;
  dropped: number;
  failures: number;
}
export interface AccountOpsEvent {
  account_id: number;
  account_name: string;
  kind: string;
  signal: string;
  http_status: number;
  first_seen: string;
  last_seen: string;
  occurrences: number;
  state: string;
  last_sent_at?: string;
  next_send_at: string;
  attempts: number;
}
export function newQualityPlan(): AccountQualityPlan {
  return {
    id: 0,
    account_id: 0,
    enabled: true,
    model: "",
    prompt: CANDY_PROMPT,
    reasoning_effort: "high",
    cron: "*/30 * * * *",
    samples: 1,
    max_results: 100,
    expected_answer: "21",
    action: "remove_groups",
    remove_group_ids: [],
    auto_restore: false,
    judge: {
      group_id: 0,
      model_id: "",
      prompt:
        "判断候选答案是否在语义上符合参考答案。忽略不影响含义的单位、标点和措辞差异，关注最终结论及题目要求。明确符合返回 correct，明确不符合返回 incorrect；无法确定或依据不足时返回 unknown。",
    },
    version: 0,
  };
}
export function formatQualityAction(action: string, kind: string) {
  if (action === "restored")
    return kind === "remove_groups" ? "已恢复原分组" : "已恢复调度";
  return (
    (
      {
        groups_removed: "已移除指定分组",
        scheduling_disabled: "已关闭调度",
        already_quarantined: "保持隔离",
        restore_conflict: "恢复冲突",
        passed: "通过",
        inconclusive: "结果不确定",
        no_change: "未变更",
        stale_run: "规则已更新，未处理",
        action_failed: "处理失败",
        capacity_unavailable: "等待检测容量",
        account_deleted: "账号已删除",
      } as Record<string, string>
    )[action] || action
  );
}
export const date = (value?: string) =>
  value ? new Date(value).toLocaleString() : "—";
