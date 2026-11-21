// Recipient-consent HTTP surface (design §6).
package authz

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func writeGovernanceError(r *http.Request, w http.ResponseWriter, err error) {
	var fail *Fail
	if errorsAsFail(err, &fail) {
		status := http.StatusBadRequest
		switch fail.Code {
		case "grantee_not_found", "team_not_found":
			status = http.StatusNotFound
		case "offer_suppressed", "offer_already_active", "state_conflict",
			"offer_stale_source_changed", "snapshot_missing_requires_review",
			"no_restorable_source", "resource_changed", "group_snapshot_too_large":
			status = http.StatusConflict
		case "team_approval_forbidden":
			status = http.StatusForbidden
		}
		if fail.Code != "" {
			kernel.WriteJSON(w, status, map[string]any{"code": fail.Code, "message": fail.Message})
			return
		}
		kernel.WriteError(w, status, fail.Message)
		return
	}
	var conflict *Conflict
	if errorsAsConflict(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Error())
		return
	}
	if errors.Is(err, errOfferSuppressed) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]any{"code": "offer_suppressed", "message": "授权请求已被抑制"})
		return
	}
	if errors.Is(err, errNoRestorable) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]any{"code": "no_restorable_source", "message": errNoRestorable.Message})
		return
	}
	kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "授权操作失败", err)
}

func (d *Deps) writeData(w http.ResponseWriter, data any) {
	kernel.WriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (d *Deps) mountGovernanceRoutes(k *kernel.Kernel, prefix string) {
	self := func(handler http.HandlerFunc) http.Handler { return d.RequireSelf(handler) }
	admin := func(handler http.HandlerFunc) http.Handler { return d.RequireAdminAuthz(handler) }

	k.Register("GET "+prefix+"/my-authorizations/notifications", self(d.listNotifications))
	k.Register("POST "+prefix+"/my-authorizations/notifications/{id}/read", self(d.markNotificationRead))
	k.Register("GET "+prefix+"/my-authorizations/inbox-summary", self(d.inboxSummary))
	k.Register("GET "+prefix+"/my-authorization-blocks", self(d.listBlocks))
	k.Register("DELETE "+prefix+"/my-authorization-blocks/{id}", self(d.deleteBlock))

	k.Register("GET "+prefix+"/my-authorization-sources/{sourceId}", self(d.sourceDetail))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/accept", self(d.acceptSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/decline", self(d.declineSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/return", self(d.returnSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/block", self(d.blockSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/bind", self(d.bindGroupSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/unbind", self(d.unbindGroupSource))
	k.Register("GET "+prefix+"/my-authorization-sources/{sourceId}/scope-diff", self(d.scopeDiff))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/refresh-scope", self(d.refreshScope))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/accounts/{accountId}/reconfirm", self(d.reconfirmScopeAccount))

	k.Register("POST "+prefix+"/my-authorizations/{id}/owner-approve", self(d.ownerApprove))
	k.Register("POST "+prefix+"/my-authorizations/{id}/owner-decline", self(d.ownerDecline))
	k.Register("POST "+prefix+"/my-authorizations/{id}/pause", self(d.pauseOffer))
	k.Register("POST "+prefix+"/my-authorizations/{id}/resume", self(d.resumeOffer))
	k.Register("POST "+prefix+"/my-authorizations/{id}/extend", self(d.extendOffer))
	k.Register("POST "+prefix+"/my-authorizations/{id}/reinvite", self(d.reinviteMember))

	k.Register("GET "+prefix+"/my-teams/{teamId}/authorization-offers", self(d.teamOffers(false)))
	k.Register("POST "+prefix+"/my-teams/{teamId}/authorization-offers/{grantId}/approve", self(d.teamApprove(false)))
	k.Register("POST "+prefix+"/my-teams/{teamId}/authorization-offers/{grantId}/decline", self(d.teamDecline(false)))
	k.Register("GET "+prefix+"/system-teams/{teamId}/authorization-offers", admin(d.teamOffers(true)))
	k.Register("POST "+prefix+"/system-teams/{teamId}/authorization-offers/{grantId}/approve", admin(d.teamApprove(true)))
	k.Register("POST "+prefix+"/system-teams/{teamId}/authorization-offers/{grantId}/decline", admin(d.teamDecline(true)))

	k.Register("POST "+prefix+"/authorizations/{id}/pause", admin(d.adminPauseOffer))
	k.Register("POST "+prefix+"/authorizations/{id}/resume", admin(d.adminResumeOffer))
	k.Register("POST "+prefix+"/authorizations/{id}/extend", admin(d.adminExtendOffer))

	k.Register("GET "+prefix+"/authorizations/blocks", admin(d.adminBlocks))
	k.Register("GET "+prefix+"/system-accounts/{id}/authorization-suspension", admin(d.getSuspension))
	k.Register("POST "+prefix+"/system-accounts/{id}/authorization-suspension", admin(d.suspendAccount))
	k.Register("DELETE "+prefix+"/system-accounts/{id}/authorization-suspension", admin(d.unsuspendAccount))
}

func (d *Deps) listNotifications(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	page, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("pageSize")))
	items, total, err := d.Store.ListNotifications(r.Context(), auth.SystemAccountID, page, pageSize)
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"items": items, "total": total})
}

func (d *Deps) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	ok, err := d.Store.MarkNotificationRead(r.Context(), auth.SystemAccountID, r.PathValue("id"), nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	if !ok {
		kernel.WriteError(w, http.StatusNotFound, "通知不存在")
		return
	}
	d.writeData(w, map[string]any{"read": true})
}

func nowISO(store *Store) string { return formatTime(store.now().UTC()) }

func (d *Deps) inboxSummary(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	summary, err := d.Store.InboxSummary(r.Context(), auth.SystemAccountID, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, summary)
}

func (d *Deps) listBlocks(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	items, err := d.Store.ListBlocks(r.Context(), auth.SystemAccountID)
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"items": items})
}

func (d *Deps) deleteBlock(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	ok, err := d.Store.DeleteBlock(r.Context(), auth.SystemAccountID, r.PathValue("id"))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	if !ok {
		kernel.WriteError(w, http.StatusNotFound, "屏蔽记录不存在")
		return
	}
	d.writeData(w, map[string]any{"deleted": true})
}

func (d *Deps) sourceDetail(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	detail, err := d.Store.GetSourceDetail(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "授权来源不存在")
		return
	}
	d.writeData(w, detail)
}

func (d *Deps) acceptSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		ExpectedRevision        *string `json:"expectedRevision"`
		ExpectedRevisionVersion *int    `json:"expectedRevisionVersion"`
		RetainFallback          *bool   `json:"retainFallback"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{
		"expectedRevision": true, "expectedRevisionVersion": true, "retainFallback": true,
	}) {
		return
	}
	if body.ExpectedRevision == nil || strings.TrimSpace(*body.ExpectedRevision) == "" ||
		body.ExpectedRevisionVersion == nil {
		kernel.WriteBadRequest(w, "请携带已查看的来源修订版本")
		return
	}
	result, err := d.Store.AcceptSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), AcceptInput{
		ExpectedRevision:        strings.TrimSpace(*body.ExpectedRevision),
		ExpectedRevisionVersion: *body.ExpectedRevisionVersion,
		RetainFallback:          body.RetainFallback,
	}, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, result)
}

func (d *Deps) declineSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.DeclineSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"status": StatusDeclined})
}

func (d *Deps) returnSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.ReturnSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"status": StatusReturned})
}

func (d *Deps) blockSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		Scope          string `json:"scope"`
		ReturnExisting *bool  `json:"returnExisting"`
		Reason         string `json:"reason"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{
		"scope": true, "returnExisting": true, "reason": true,
	}) {
		return
	}
	// Contract default is return_existing=true; only an explicit false keeps
	// existing sources active.
	returnExisting := true
	if body.ReturnExisting != nil {
		returnExisting = *body.ReturnExisting
	}
	result, err := d.Store.BlockSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), BlockInput{
		Scope: body.Scope, ReturnExisting: returnExisting, Reason: body.Reason,
	}, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, result)
}

func (d *Deps) bindGroupSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.BindGroupSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), true, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"enabled": true})
}

func (d *Deps) unbindGroupSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.BindGroupSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), false, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"enabled": false})
}

func (d *Deps) scopeDiff(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	diff, err := d.Store.ScopeDiff(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, diff)
}

func (d *Deps) refreshScope(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var input RefreshScopeInput
	if !decodeStrictJSON(w, r, &input, map[string]bool{
		"expectedSnapshotRevision": true, "reviewedAdded": true, "rebuildMissingSnapshot": true,
	}) {
		return
	}
	diff, err := d.Store.RefreshScope(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), input, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, diff)
}

func (d *Deps) reconfirmScopeAccount(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		ExpectedRevision        string `json:"expectedRevision"`
		ExpectedRevisionVersion int    `json:"expectedRevisionVersion"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{
		"expectedRevision": true, "expectedRevisionVersion": true,
	}) {
		return
	}
	diff, err := d.Store.ReconfirmScopeAccount(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"),
		r.PathValue("accountId"), strings.TrimSpace(body.ExpectedRevision), body.ExpectedRevisionVersion, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, diff)
}

func (d *Deps) ownerApprove(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.OwnerApprove(r.Context(), auth.SystemAccountID, r.PathValue("id"), nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"approved": true})
}

func (d *Deps) ownerDecline(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.OwnerDecline(r.Context(), auth.SystemAccountID, r.PathValue("id"), nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"declined": true})
}

func (d *Deps) pauseOffer(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.SetOfferPaused(r.Context(), r.PathValue("id"), auth.SystemAccountID, true, false, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"status": StatusPaused})
}

func (d *Deps) resumeOffer(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.SetOfferPaused(r.Context(), r.PathValue("id"), auth.SystemAccountID, false, false, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"status": StatusActive})
}

func (d *Deps) extendOffer(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		ExpiresAt                string `json:"expiresAt"`
		ConfirmExpiredFromStatus string `json:"confirmExpiredFromStatus"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{
		"expiresAt": true, "confirmExpiredFromStatus": true,
	}) {
		return
	}
	if err := d.Store.ExtendOffer(r.Context(), auth.SystemAccountID, r.PathValue("id"), ExtendInput{
		ExpiresAt: body.ExpiresAt, ConfirmExpiredFromStatus: body.ConfirmExpiredFromStatus,
	}, false, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"extended": true})
}

func (d *Deps) reinviteMember(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		MemberID string `json:"memberId"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{"memberId": true}) {
		return
	}
	if strings.TrimSpace(body.MemberID) == "" {
		kernel.WriteBadRequest(w, "请选择要重新邀请的团队成员")
		return
	}
	sourceID, err := d.Store.Reinvite(r.Context(), r.PathValue("id"), strings.TrimSpace(body.MemberID), auth.SystemAccountID, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"sourceId": sourceID})
}

func (d *Deps) teamOffers(adminView bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		teamID := r.PathValue("teamId")
		if !adminView {
			ok, err := d.Store.CanViewTeamOffers(r.Context(), teamID, auth.SystemAccountID)
			if err != nil {
				writeGovernanceError(r, w, err)
				return
			}
			if !ok {
				kernel.WriteError(w, http.StatusForbidden, "无权查看该团队授权")
				return
			}
		}
		items, err := d.Store.ListTeamOffers(r.Context(), teamID)
		if err != nil {
			writeGovernanceError(r, w, err)
			return
		}
		d.writeData(w, map[string]any{"items": items})
	}
}

func (d *Deps) teamApprove(adminView bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if err := d.Store.TeamApprove(r.Context(), r.PathValue("teamId"), r.PathValue("grantId"),
			auth.SystemAccountID, adminView, nowISO(d.Store)); err != nil {
			writeGovernanceError(r, w, err)
			return
		}
		d.writeData(w, map[string]any{"approved": true})
	}
}

func (d *Deps) teamDecline(adminView bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if err := d.Store.TeamDecline(r.Context(), r.PathValue("teamId"), r.PathValue("grantId"),
			auth.SystemAccountID, adminView, nowISO(d.Store)); err != nil {
			writeGovernanceError(r, w, err)
			return
		}
		d.writeData(w, map[string]any{"declined": true})
	}
}

func (d *Deps) adminPauseOffer(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.SetOfferPaused(r.Context(), r.PathValue("id"), auth.SystemAccountID, true, true, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"status": StatusPaused})
}

func (d *Deps) adminResumeOffer(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if err := d.Store.SetOfferPaused(r.Context(), r.PathValue("id"), auth.SystemAccountID, false, true, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"status": StatusActive})
}

func (d *Deps) adminExtendOffer(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		ExpiresAt                string `json:"expiresAt"`
		ConfirmExpiredFromStatus string `json:"confirmExpiredFromStatus"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{
		"expiresAt": true, "confirmExpiredFromStatus": true,
	}) {
		return
	}
	if err := d.Store.ExtendOffer(r.Context(), auth.SystemAccountID, r.PathValue("id"), ExtendInput{
		ExpiresAt: body.ExpiresAt, ConfirmExpiredFromStatus: body.ConfirmExpiredFromStatus,
	}, true, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"extended": true})
}

func (d *Deps) adminBlocks(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.AdminListBlocks(r.Context())
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"items": items})
}

func (d *Deps) getSuspension(w http.ResponseWriter, r *http.Request) {
	status, err := d.Store.GetSuspension(r.Context(), r.PathValue("id"))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, status)
}

func (d *Deps) suspendAccount(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{"reason": true}) {
		return
	}
	if err := d.Store.Suspend(r.Context(), r.PathValue("id"), auth.SystemAccountID, body.Reason, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"suspended": true})
}

func (d *Deps) unsuspendAccount(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	ok, err := d.Store.Unsuspend(r.Context(), r.PathValue("id"), auth.SystemAccountID, nowISO(d.Store))
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"lifted": ok})
}

// CanViewTeamOffers allows the team approver and administrators only.
func (s *Store) CanViewTeamOffers(ctx context.Context, teamID, actorID string) (bool, error) {
	var creator string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+`
		WHERE id = ? AND status = 'active' AND deleted_at IS NULL`), teamID).Scan(&creator)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return creator == actorID, nil
}
