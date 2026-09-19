package chat

import (
	"errors"
)

// Compaction-domain store surface retained in the facade root after the
// chatassets split (REFACTOR-0006 phase C): CompactionSourcePage embeds
// contextSourceMessage (context.go vocabulary) and the loader depends on
// findCompactionClaim / maxContextLoadBytes, so migrating it would pull the
// context subdomain into chatassets. The function body below is
// byte-identical to the pre-split text.

// CompactionSourcePage mirrors ChatCompactionSourcePage.
type CompactionSourcePage struct {
	Messages           []contextSourceMessage
	NextAfterSequence  int64
	LoadedBytes        int64
	EarliestExpiresAt  string
	BlockedByByteLimit bool
}

// LoadCompactionSourcePage mirrors loadChatCompactionSourcePage.
func (s *Store) LoadCompactionSourcePage(conversationID, ownerID, claimID string, afterSequence int64, nowValue string, limit, maxBytes int) (*CompactionSourcePage, error) {
	now, err := requireRFC3339Instant(nowValue, "聊天上下文 now")
	if err != nil {
		return nil, err
	}
	if afterSequence < 0 {
		return nil, &DomainError{Message: "afterSequence 必须是非负安全整数"}
	}
	if limit < 2 || limit > 512 {
		return nil, &DomainError{Message: "limit 必须是 2..512 的整数"}
	}
	if maxBytes < 1 || maxBytes > maxContextLoadBytes {
		return nil, &DomainError{Message: "maxBytes 必须是 1..16777216 的整数"}
	}
	claim, err := s.findCompactionClaim(s.db, conversationID, ownerID, claimID)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, nil
	}
	if afterSequence != claim.ProgressSequence {
		return nil, errors.New("压缩来源游标超出当前认领范围")
	}
	rowBudget := limit - limit%2
	messagesTable := s.table("chat_messages")
	rows, err := s.db.Query(s.bind(`SELECT source.id, source.turn_id, source.sequence_no, source.role, source.content_text,
			source.content_blocks_json, source.content_bytes, source.model, source.created_at, source.completed_at, source.expires_at
		FROM `+messagesTable+` AS source
		WHERE source.conversation_id = ? AND source.system_account_id = ?
			AND source.status = 'completed' AND source.expires_at > ?
			AND source.sequence_no > ? AND source.sequence_no <= ?
			AND EXISTS (
				SELECT 1 FROM `+messagesTable+` AS pair
				WHERE pair.conversation_id = source.conversation_id
					AND pair.system_account_id = source.system_account_id
					AND pair.turn_id = source.turn_id
					AND pair.status = 'completed' AND pair.expires_at > ?
					AND (
						(source.role = 'user' AND pair.role = 'assistant' AND pair.sequence_no = source.sequence_no + 1)
						OR (source.role = 'assistant' AND pair.role = 'user' AND pair.sequence_no = source.sequence_no - 1)
					)
			)
		ORDER BY source.sequence_no ASC
		LIMIT ?`), conversationID, ownerID, now, afterSequence, claim.SourceThroughSequence, now, rowBudget+2)
	if err != nil {
		return nil, err
	}
	loaded := []contextSourceMessage{}
	for rows.Next() {
		var message contextSourceMessage
		if err := rows.Scan(&message.id, &message.turnID, &message.sequenceNo, &message.role, &message.contentText,
			&message.contentBlocksJSON, &message.contentBytes, &message.modelID, &message.createdAt,
			&message.completedAt, &message.expiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		message.contentBytes = maxI64(message.contentBytes, int64(len(message.contentText)+len(message.contentBlocksJSON)))
		loaded = append(loaded, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	page := &CompactionSourcePage{Messages: []contextSourceMessage{}}
	var loadedBytes int64
	earliest := ""
	blocked := false
	index := 0
	for ; index+1 < len(loaded) && index < rowBudget; index += 2 {
		user := loaded[index]
		assistant := loaded[index+1]
		if user.role != "user" || assistant.role != "assistant" || user.turnID != assistant.turnID || assistant.sequenceNo != user.sequenceNo+1 {
			return nil, errors.New("压缩来源完整轮次顺序不一致")
		}
		pairBytes := user.contentBytes + assistant.contentBytes
		if loadedBytes+pairBytes > int64(maxBytes) && len(page.Messages) > 0 {
			blocked = true
			break
		}
		if pairBytes > maxContextLoadBytes {
			return nil, errors.New("单个完整聊天轮次超过压缩来源绝对大小限制")
		}
		page.Messages = append(page.Messages, user, assistant)
		loadedBytes += pairBytes
		for _, message := range []contextSourceMessage{user, assistant} {
			if earliest == "" || message.expiresAt < earliest {
				earliest = message.expiresAt
			}
		}
	}
	if !blocked {
		if index < len(loaded) && index < rowBudget {
			blocked = true
		} else if len(loaded) > rowBudget {
			blocked = true
		}
	}
	page.LoadedBytes = loadedBytes
	page.BlockedByByteLimit = blocked
	page.NextAfterSequence = afterSequence
	if len(page.Messages) > 0 {
		page.NextAfterSequence = page.Messages[len(page.Messages)-1].sequenceNo
	}
	page.EarliestExpiresAt = earliest
	return page, nil
}
