-- Verifies that the database and the filesystem agree about what exists.
--
-- Run with:  make db-check
--
-- All five counts should be zero except live_nodes and blobs. Anything else
-- means the two halves of the storage design have diverged; see
-- docs/troubleshooting.md and consider running the reconciliation job.

\pset title 'PiFiles consistency check'

SELECT
    -- Informational.
    (SELECT count(*) FROM nodes WHERE deleted_at IS NULL)                      AS live_nodes,
    (SELECT count(*) FROM nodes WHERE deleted_at IS NOT NULL)                  AS trashed_nodes,
    (SELECT count(*) FROM blobs)                                               AS blobs,

    -- A file whose content is missing from disk. This is data loss: the file
    -- cannot be downloaded. Restore from backup.
    (SELECT count(*) FROM nodes n
      WHERE n.content_hash IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM blobs b WHERE b.hash = n.content_hash))  AS files_missing_content,

    -- A reference count that disagrees with what actually references the blob.
    -- Too low risks deleting content that is still in use; too high wastes
    -- space forever. Repaired by reconciliation.
    --
    -- Both kinds of reference count: a node pointing at the content, and a
    -- version of a file that used to. Counting only nodes would report every
    -- versioned file as an error.
    (SELECT count(*) FROM blobs b
     WHERE b.ref_count <> (
         (SELECT count(*) FROM nodes n WHERE n.content_hash = b.hash)
       + (SELECT count(*) FROM versions v WHERE v.content_hash = b.hash)
     ))                                                                        AS refcount_errors,

    -- A stored usage total that disagrees with the user's actual content.
    -- Repaired by the quota-recalculation job.
    (SELECT count(*) FROM users u
      WHERE u.used_bytes <> (
        SELECT coalesce(sum(size_bytes), 0) FROM nodes
         WHERE owner_id = u.id AND kind = 'file' AND deleted_at IS NULL))      AS quota_mismatches,

    -- A node whose parent is missing, which would make it unreachable.
    (SELECT count(*) FROM nodes n
      WHERE n.parent_id IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM nodes p WHERE p.id = n.parent_id))       AS orphaned_nodes,

    -- Every account must have exactly one root folder.
    (SELECT count(*) FROM users u
      WHERE (SELECT count(*) FROM nodes n
              WHERE n.owner_id = u.id AND n.parent_id IS NULL) <> 1)           AS bad_root_folders;
