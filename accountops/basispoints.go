package accountops

import (
	"context"
	"fmt"
	"time"
)

func IsBasispointsCategory(kind string) bool {
	switch kind {
	case "auto_403_disabled", "image_capacity_rejected", "image_cleanup_failed", "recovery_suggested":
		return true
	}
	return false
}
func ValidBasispointsEvent(e AccountOpsEvent) bool {
	return IsBasispointsCategory(e.Kind) && ((e.AccountID > 0 && e.CredentialGeneration >= 0) || (e.AccountID == 0 && e.CredentialGeneration == 0 && e.Kind == "image_cleanup_failed"))
}

// ObserveBasispoints accepts only fixed categories, never caller error text or URLs.
func (s *AccountOpsService) ObserveBasispoints(id, generation int64, category string) {
	if s == nil || !s.moduleEnabled() {
		return
	}
	e := AccountOpsEvent{AccountID: id, CredentialGeneration: generation, Kind: category, Signal: category}
	if !ValidBasispointsEvent(e) {
		return
	}
	if category == "auto_403_disabled" {
		e.HTTPStatus = 403
	}
	select {
	case s.queue <- e:
	default:
		s.dropped.Add(1)
	}
}

// Configure before Start. The callback returns sent=false when Bark is not opted in.
func (s *AccountOpsService) SetBasispointsNotifier(notify func(context.Context, *AccountOpsEvent) (bool, error)) {
	s.basispointsNotifier = notify
}
func (s *AccountOpsService) allowsEvent(kind string) bool {
	if IsBasispointsCategory(kind) {
		return s.moduleEnabled()
	}
	return s.currentConfig().Allows(kind)
}
func (s *AccountOpsService) deliverBasispointsEvent(ctx context.Context, e *AccountOpsEvent) {
	state := "suppressed"
	delay := time.Duration(s.currentConfig().CooldownMinutes) * time.Minute
	if delay < 5*time.Minute {
		delay = time.Hour
	}
	if s.moduleEnabled() && ValidBasispointsEvent(*e) && s.basispointsNotifier != nil {
		send, stop := context.WithTimeout(ctx, 15*time.Second)
		sent, err := s.basispointsNotifier(send, e)
		stop()
		if err != nil {
			state = "failed"
			if e.Attempts < 3 {
				delay = 5 * time.Minute
			}
		} else if sent {
			state = "sent"
		}
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err := s.repo.Complete(finish, e, state, delay); err != nil {
		s.failures.Add(1)
	}
}
func BasispointsNotification(e AccountOpsEvent) (string, string) {
	label := "BPS 路径提醒"
	switch e.Kind {
	case "auto_403_disabled":
		label = "BPS 路径已自动关闭"
	case "image_capacity_rejected":
		label = "BPS 图片中转容量不足"
	case "image_cleanup_failed":
		label = "BPS 临时图片清理失败"
	case "recovery_suggested":
		label = "BPS 路径建议人工检查恢复"
	}
	return label, fmt.Sprintf("账号 #%d；凭证版本 %d；事件 %s；累计 %d 次。请在账号运维中查看。", e.AccountID, e.CredentialGeneration, e.Kind, e.Occurrences)
}
