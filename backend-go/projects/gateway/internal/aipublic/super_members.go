package aipublic

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// updateSuperMembers 服务 POST /__aipublic__/group/super-members/update
// （超级分组设计 §5）：严格接受 {targetUsername, superGroupId,
// expectedRevision, members:[{memberGroupId,priority,enabled}]}，规则与管理面
// 成员替换完全相同，authorization scope 为
// juhe_ai_public:group_super_members_update:write。
func (d *Deps) updateSuperMembers(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var body map[string]json.RawMessage
	if err := decoder.Decode(&body); err != nil {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "超级分组成员参数无效")
		return
	}
	for key := range body {
		switch key {
		case "targetUsername", "superGroupId", "expectedRevision", "members":
		default:
			writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "超级分组成员参数无效")
			return
		}
	}
	targetUsername, ok := rawTrimmedString(body, "targetUsername", 2, 80)
	if !ok {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "目标用户不能为空")
		return
	}
	superGroupID, ok := rawTrimmedString(body, "superGroupId", 1, 120)
	if !ok {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "超级分组不能为空")
		return
	}
	expectedRevision, ok := rawTrimmedString(body, "expectedRevision", 1, 128)
	if !ok {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "expectedRevision 不能为空")
		return
	}
	members, parseOK := rawSuperMembers(body["members"])
	if !parseOK {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "超级分组成员参数无效")
		return
	}
	if AuthContextFrom(r).IsTestToken {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "测试令牌不支持超级分组成员更新")
		return
	}
	owner, err := d.findGroupOwnerByID(r.Context(), superGroupID)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), targetUsername, true, owner)
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	if target.Account.Status != "active" {
		writeCodeError(w, http.StatusBadRequest, groups.CodeSuperGroupMemberInvalid, "目标用户已停用："+target.Account.Username)
		return
	}
	summary, err := d.Groups.ReplaceSuperMembers(r.Context(), superGroupID, expectedRevision, members, groups.AccessScope{ViewerID: target.Account.ID})
	if err != nil {
		if codeErr, isCode := err.(*groups.CodeError); isCode {
			writeCodeError(w, codeErr.Status, codeErr.Code, codeErr.Message)
			return
		}
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "updated", "target": target.Public,
		"superGroupId": summary.SuperGroupID, "revision": summary.Revision, "members": summary.Members,
	})
}

// rawTrimmedString 解析必填字符串字段并做长度校验。
func rawTrimmedString(body map[string]json.RawMessage, key string, minLen, maxLen int) (string, bool) {
	raw, exists := body[key]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	if len(value) < minLen || len(value) > maxLen {
		return "", false
	}
	return value, true
}

// rawSuperMembers 严格解析成员数组：未知键、缺键、非整数或负数 priority 均拒绝。
func rawSuperMembers(raw json.RawMessage) ([]groups.SuperMemberInput, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, false
	}
	members := make([]groups.SuperMemberInput, 0, len(rows))
	for _, row := range rows {
		for key := range row {
			switch key {
			case "memberGroupId", "priority", "enabled":
			default:
				return nil, false
			}
		}
		memberIDRaw, hasID := row["memberGroupId"]
		priorityRaw, hasPriority := row["priority"]
		enabledRaw, hasEnabled := row["enabled"]
		if !hasID || !hasPriority || !hasEnabled {
			return nil, false
		}
		var memberID string
		if err := json.Unmarshal(memberIDRaw, &memberID); err != nil || strings.TrimSpace(memberID) == "" {
			return nil, false
		}
		var priority json.Number
		if err := json.Unmarshal(priorityRaw, &priority); err != nil {
			return nil, false
		}
		priorityValue, err := priority.Int64()
		if err != nil || priorityValue < 0 {
			return nil, false
		}
		var enabled bool
		if err := json.Unmarshal(enabledRaw, &enabled); err != nil {
			return nil, false
		}
		members = append(members, groups.SuperMemberInput{
			MemberGroupID: strings.TrimSpace(memberID),
			Priority:      int(priorityValue),
			Enabled:       enabled,
		})
	}
	return members, true
}
