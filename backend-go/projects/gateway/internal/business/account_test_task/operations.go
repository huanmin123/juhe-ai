package accounttesttask

// CoveredManifestOperations names the DbService operations whose task-table
// semantics are implemented by this package. This list is evidence only; it
// must not be used to mark the transaction group as handoff-complete.
var CoveredManifestOperations = []string{
	"account_test_task_maintenance",
	"mark_account_test_task_running",
	"mark_account_test_task_canceled",
	"complete_account_test_task",
	"fail_account_test_task",
	"update_account_test_task_message",
	"is_account_test_task_cancel_requested",
	"read_account_test_task_cancel_message",
}

// OutstandingManifestOperations remain outside this package because they
// require the account test-read model and the account-runtime transaction.
// They deliberately keep the `account-test-task` capability manifest status
// at partial until the same owner implements and verifies them.
var OutstandingManifestOperations = []string{
	"find_account_for_test",
	"mark_account_test_temporary_unavailable",
}
