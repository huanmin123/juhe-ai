package contracts

// ArchitectureVersion identifies the cross-project contract generation.
// It is deliberately independent from any business module or deployment release.
const ArchitectureVersion = "go-projects-v1"

type ProjectID string

const (
	ProjectGateway     ProjectID = "gateway"
	ProjectJobs        ProjectID = "jobs"
	ProjectMaintenance ProjectID = "maintenance"
)

type JobPriority string

const (
	PriorityRealtime JobPriority = "realtime"
	PriorityBatch    JobPriority = "batch"
	PriorityCold     JobPriority = "cold"
)

// JobIdentity is the stable identity shared by job registration and reports.
// Scheduling policy and implementation stay inside the jobs project.
type JobIdentity struct {
	ID       string
	Priority JobPriority
}
