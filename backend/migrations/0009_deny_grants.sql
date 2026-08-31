-- 0009_deny_grants: let a subject hold both a grant and a denial on one node.
--
-- The original index was UNIQUE (node_id, subject_type, subject_id) — one row
-- per subject per node. That is right for the case it was written for:
-- re-sharing with somebody should update their grant rather than accumulate a
-- second one nobody would think to revoke.
--
-- It is wrong for denials, and the evaluator has always assumed so. Evaluate()
-- accumulates `granted` and `denied` separately and subtracts one from the
-- other, which only means anything if both can exist at once. With one row per
-- subject, denying Bob the delete permission *replaced* his grant instead of
-- reducing it — so "you may read this but not delete it" silently became "you
-- may do nothing at all".
--
-- Nobody noticed because until Phase 13 the API could not create a denial at
-- all; they existed only in tests that wrote to the table directly, and those
-- never gave the same subject both.
--
-- Including `deny` in the index keeps the original intent for each kind — one
-- grant and one denial per subject per node, each updated in place on repeat —
-- while letting them coexist as the evaluator always expected.

DROP INDEX permissions_unique_grant_idx;

CREATE UNIQUE INDEX permissions_unique_grant_idx
    ON permissions (node_id, subject_type, subject_id, deny);

COMMENT ON INDEX permissions_unique_grant_idx IS
    'One grant and one denial per subject per node. A denial reduces a grant rather than replacing it.';
