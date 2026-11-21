// Owner PATCH contract: remark/limits and expiry shortening only; extension
// has its own explicit endpoint (design §5.5/§6.1).
package authz

import (
	"context"
	"strings"
)

type UpdateOfferInput struct {
	Remark       *string
	LimitsJSON   *string
	LimitsSet    bool
	ExpiresAt    *string
	ExpiresAtSet bool
}

func (s *Store) UpdateOffer(ctx context.Context, actorID, grantID string, input UpdateOfferInput, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if grant == nil || grant.OwnerID != actorID {
		return failf("授权不存在")
	}
	if grant.Status != StatusActive && grant.Status != StatusPaused {
		return failCode("state_conflict", "只有生效或暂停中的授权可以修改额度与备注")
	}
	nowTime := parseTimeOrNow(now)
	nextExpires := grant.ExpiresAt
	if input.ExpiresAtSet {
		if input.ExpiresAt == nil || strings.TrimSpace(*input.ExpiresAt) == "" {
			if grant.ExpiresAt != "" {
				return failCode("extension_requires_extend", "取消到期时间属于延长，请使用延长接口")
			}
		} else {
			normalized, err := normalizeAuthorizationExpiresAt(input.ExpiresAt)
			if err != nil {
				return err
			}
			if normalized == nil || !parseTimeOrNow(*normalized).After(nowTime) {
				return failf("新的到期时间必须晚于当前时间")
			}
			if grant.ExpiresAt != "" {
				current := parseTimeOrNow(grant.ExpiresAt)
				next := parseTimeOrNow(*normalized)
				if next.After(current) {
					return failCode("extension_requires_extend", "延长到期时间请使用延长接口")
				}
			}
			nextExpires = *normalized
		}
	}
	limits := grant.LimitsJSON
	if input.LimitsSet {
		if input.LimitsJSON == nil || strings.TrimSpace(*input.LimitsJSON) == "" {
			limits = ""
		} else {
			normalized, err := normalizeAuthorizationLimitsJSON(*input.LimitsJSON)
			if err != nil {
				return err
			}
			if normalized == nil {
				limits = ""
			} else {
				limits = *normalized
			}
		}
		if limits != grant.LimitsJSON {
			// 暂停发起授权期间不得通过额度 PATCH 绕过冻结；缩短到期时间与
			// 备注不经过这里，仍允许。
			if err := s.ensureNotSuspended(ctx, tx, grant.OwnerID); err != nil {
				return err
			}
		}
	}
	remark := grant.Remark
	if input.Remark != nil {
		remark = strings.TrimSpace(*input.Remark)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET remark = ?, limits_json = NULLIF(?, ''), expires_at = NULLIF(?, ''), updated_at = ?
		WHERE id = ? AND status IN ('active','paused')`),
		nullIfEmpty(remark), limits, nextExpires, now, grant.ID); err != nil {
		return err
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, actorID, now); err != nil {
		return err
	}
	grant.Remark = remark
	grant.LimitsJSON = limits
	grant.ExpiresAt = nextExpires
	if err := s.syncRuntimeQuotaBindings(ctx, tx, grant, now); err != nil {
		return err
	}
	for _, target := range s.grantRuntimeGrantees(ctx, tx, grant.ID) {
		if err := s.insertNotification(ctx, tx, target, "offer_updated", grant.ID, "", nil,
			"updated:"+grant.ID+":"+target+":"+now, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}
