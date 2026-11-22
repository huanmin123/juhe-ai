// Recipient claim HTTP surface: the simplified send-and-claim model.
package authz

import (
	"errors"
	"net/http"
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
		case "offer_already_active", "state_conflict",
			"no_restorable_source", "resource_changed":
			status = http.StatusConflict
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

	k.Register("GET "+prefix+"/my-authorizations/inbox-summary", self(d.inboxSummary))

	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/accept", self(d.acceptSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/decline", self(d.declineSource))
	k.Register("POST "+prefix+"/my-authorization-sources/{sourceId}/return", self(d.returnSource))

	k.Register("POST "+prefix+"/my-authorizations/{id}/pause", self(d.pauseOffer))
	k.Register("POST "+prefix+"/my-authorizations/{id}/resume", self(d.resumeOffer))
	k.Register("POST "+prefix+"/my-authorizations/{id}/extend", self(d.extendOffer))

	k.Register("POST "+prefix+"/authorizations/{id}/pause", admin(d.adminPauseOffer))
	k.Register("POST "+prefix+"/authorizations/{id}/resume", admin(d.adminResumeOffer))
	k.Register("POST "+prefix+"/authorizations/{id}/extend", admin(d.adminExtendOffer))
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

func (d *Deps) acceptSource(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	var body struct {
		TargetGroupID *string `json:"targetGroupId"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{"targetGroupId": true}) {
		return
	}
	input := AcceptInput{}
	if body.TargetGroupID != nil {
		input.TargetGroupID = strings.TrimSpace(*body.TargetGroupID)
	}
	result, err := d.Store.AcceptSource(r.Context(), auth.SystemAccountID, r.PathValue("sourceId"), input, nowISO(d.Store))
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
		ExpiresAt string `json:"expiresAt"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{"expiresAt": true}) {
		return
	}
	if err := d.Store.ExtendOffer(r.Context(), auth.SystemAccountID, r.PathValue("id"), ExtendInput{
		ExpiresAt: body.ExpiresAt,
	}, false, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"extended": true})
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
		ExpiresAt string `json:"expiresAt"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{"expiresAt": true}) {
		return
	}
	if err := d.Store.ExtendOffer(r.Context(), auth.SystemAccountID, r.PathValue("id"), ExtendInput{
		ExpiresAt: body.ExpiresAt,
	}, true, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"extended": true})
}
