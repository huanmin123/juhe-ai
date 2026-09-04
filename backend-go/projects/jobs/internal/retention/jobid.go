package retention

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
)

// pseudoRandomUint32 mirrors the random tail of Node newId(): eight hex
// characters from a random source.
func pseudoRandomUint32() uint32 {
	var buffer [4]byte
	if _, err := rand.Read(buffer[:]); err == nil {
		return binary.LittleEndian.Uint32(buffer[:])
	}
	return uint32(time.Now().UnixNano())
}

// DecodeRecordMaintenanceJob mirrors normalizeRecordMaintenanceStreamJob's
// isRecordMaintenanceJob guard: the payload must decode into the job shape
// and satisfy the discriminator-specific field checks before it may run.
func DecodeRecordMaintenanceJob(payload []byte) (RecordMaintenanceJob, error) {
	var raw struct {
		Type              string         `json:"type"`
		ID                string         `json:"id"`
		APIKeyID          string         `json:"apiKeyId"`
		AccountID         string         `json:"accountId"`
		SystemAccountID   string         `json:"systemAccountId"`
		RelatedAccountIDs []string       `json:"relatedAccountIds"`
		AuthorizationIDs  []string       `json:"authorizationIds"`
		TeamScopeIDs      []string       `json:"teamScopeIds"`
		CutoffAt          string         `json:"cutoffAt"`
		BatchSize         *float64       `json:"batchSize"`
		MaxBatches        *float64       `json:"maxBatches"`
		Kind              string         `json:"kind"`
		Source            string         `json:"source"`
		Snapshot          map[string]any `json:"snapshot"`
		UpdatedAt         string         `json:"updatedAt"`
		CreatedAt         string         `json:"createdAt"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return RecordMaintenanceJob{}, fmt.Errorf("Redis Stream 数据维护消息格式无效")
	}
	job := RecordMaintenanceJob{
		Type:              raw.Type,
		ID:                raw.ID,
		APIKeyID:          raw.APIKeyID,
		AccountID:         raw.AccountID,
		SystemAccountID:   raw.SystemAccountID,
		RelatedAccountIDs: raw.RelatedAccountIDs,
		AuthorizationIDs:  raw.AuthorizationIDs,
		TeamScopeIDs:      raw.TeamScopeIDs,
		CutoffAt:          raw.CutoffAt,
		Kind:              raw.Kind,
		Source:            raw.Source,
		Snapshot:          raw.Snapshot,
		UpdatedAt:         raw.UpdatedAt,
		CreatedAt:         raw.CreatedAt,
	}
	if raw.BatchSize != nil {
		job.BatchSize = int(*raw.BatchSize)
	}
	if raw.MaxBatches != nil {
		job.MaxBatches = int(*raw.MaxBatches)
	}
	if err := ValidateRecordMaintenanceJob(job); err != nil {
		return RecordMaintenanceJob{}, err
	}
	return job, nil
}
