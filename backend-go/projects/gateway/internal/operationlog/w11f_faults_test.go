package operationlog

import (
	"errors"
)

var w11fBoom = errors.New("w11f boom")

func w11fGates() LegacyMigrationOptions {
	return LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true}
}

func w11fInput(id string) Input {
	return Input{
		ID: id, ActorSystemAccountID: "w11f-actor", ActorRole: "admin", Mode: "admin",
		Module: "w11f", Action: "test", OperationKey: "w11f.test", ResourceType: "test",
		ResourceID: id, Summary: "w11f 摘要 " + id, DetailLevel: "full", VisibilityScope: "admin_only",
		CreatedAt: "2026-09-01T00:00:00.000Z",
	}
}
