package statsverify

import (
)

// Golden derivations follow group-account-stats-cache.repository.ts:
//   - dirty consumption: '__all__' full rebuild first (cursor stored in the
//     reason prefix 'all_cursor:'), then per-group rows oldest-updated first;
//   - counting: enabled=1 and not deleted and authorized (active
//     authorization not expired at updatedAt, or account system account ==
//     group system account); active+schedulable+past-cooldown => available;
//     rate_limited counts as error AND rate_limited; concurrency_limit sums;
//   - cache rewrite: DELETE per group chunk then INSERT (empty groups keep a
//     zero row);
//   - dirty rows are deleted with an updated_at CAS.

