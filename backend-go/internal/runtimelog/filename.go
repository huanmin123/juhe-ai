package runtimelog

import "regexp"

var (
	runtimeLogRotationSuffixPattern = regexp.MustCompile(`(?i)^(.*)\.(\d{8}T\d{6}Z)\.([0-9a-f-]+)\.log$`)
	runtimeLogWorkerInstancePattern = regexp.MustCompile(`^juhe-ai\.(worker|db-service|ingest-worker|usage-worker|log-worker|stats-worker|ops-worker|temporary-maintenance-worker)\.([A-Za-z0-9][A-Za-z0-9._-]{0,63})\.log$`)
	runtimeLogServerInstancePattern = regexp.MustCompile(`^juhe-ai\.([A-Za-z0-9][A-Za-z0-9._-]{0,63})\.log$`)
)

var legacyRuntimeLogRoles = map[string]string{
	"juhe-ai.log":                              "server",
	"juhe-ai.worker.log":                       "worker",
	"juhe-ai.db-service.log":                   "db-service",
	"juhe-ai.ingest-worker.log":                "ingest-worker",
	"juhe-ai.usage-worker.log":                 "usage-worker",
	"juhe-ai.log-worker.log":                   "log-worker",
	"juhe-ai.stats-worker.log":                 "stats-worker",
	"juhe-ai.ops-worker.log":                   "ops-worker",
	"juhe-ai.temporary-maintenance-worker.log": "temporary-maintenance-worker",
}

func ParseLogFileName(name string) (role string, kind LogFileKind, ok bool) {
	if match := runtimeLogRotationSuffixPattern.FindStringSubmatch(name); match != nil {
		role, ok = currentLogFileRole(match[1] + ".log")
		return role, LogFileRotated, ok
	}
	role, ok = currentLogFileRole(name)
	return role, LogFileCurrent, ok
}

func currentLogFileRole(name string) (string, bool) {
	if role, ok := legacyRuntimeLogRoles[name]; ok {
		return role, true
	}
	if match := runtimeLogWorkerInstancePattern.FindStringSubmatch(name); match != nil {
		return match[1] + ":" + match[2], true
	}
	if match := runtimeLogServerInstancePattern.FindStringSubmatch(name); match != nil {
		return "server:" + match[1], true
	}
	return "", false
}
