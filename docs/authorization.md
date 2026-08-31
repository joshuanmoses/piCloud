# Authorization

Authorization is a separate layer from authentication. Authentication answers
"who is this?"; authorization answers "may they do this to that?".

The full permission model is implemented: ownership, direct grants, inheritance
down the folder tree, grants held through group membership, and explicit
denials. Sharing itself is documented in [sharing.md](sharing.md).

---

## 1. The rules that do not change

**Every check happens on the server.** The interface hides actions a user cannot
perform, but that is a usability affordance, never a control. Bypassing the
frontend — with `curl`, with the developer console, by editing a request —
gains access to nothing.

**A resource the caller may not see returns 404, never 403.** A distinguishable
"forbidden" would confirm that a guessed UUID names a real object, turning every
endpoint into an enumeration oracle. This is tested: the response to another
user's node and the response to a nonexistent one are asserted byte-identical.

**Identifiers are UUIDs.** Sequential integers would leak how many files exist
and make guessing tractable.

**Administrators do not get blanket read access to content.** An administrator
manages accounts, quotas, and the server. That is not the same as being entitled
to read what people store on it, and the audit log would have no way to
distinguish legitimate support from snooping. Administration routes are gated by
`RequireAdmin`; content routes are not affected by the flag. This is tested.

---

## 2. The middleware chain

```text
Authenticate            resolve the session cookie, attach the user; never rejects
      ↓
RequireAuth             reject anonymous callers with 401
      ↓
RequirePasswordChange   block normal API use while a forced change is outstanding
      ↓
CSRF                    reject state-changing requests without a valid token
      ↓
requireAccess           per-object permission check, inside the handler
```

The last step is the one that matters. The first four are coarse gates; none of
them knows anything about a specific file.

Two groups are registered outside the ordinary chain, deliberately:

- **Transfers** (upload, chunk) carry the identical protection but are *not*
  wrapped in the 30-second timeout applied to JSON handlers. A 20 GB upload over
  a home connection legitimately takes hours.
- **Downloads** skip CSRF, because they are `GET` requests. Authentication and
  the per-object check still apply.

---

## 3. The access check

Every content handler calls `requireAccess` with the permission the operation
needs. No handler performs its own check.

1. Load the node. Absent → 404.
2. Evaluate the caller's effective permissions.
3. Cannot even `read` it → **404**, indistinguishable from not existing.
4. Can read it but lacks the required permission → **403**, naming what is
   missing. Returning 404 for an item the user is looking at would be actively
   confusing, and reveals nothing they did not already know.
5. Return the node and the decision.

Which permission each operation requires:

| Operation | Requires |
| --- | --- |
| List a folder, read metadata | `read` |
| Download | `download` |
| Upload a new file | `upload` on the destination |
| Create a folder | `create` on the parent |
| Replace file content | `modify` |
| Rename | `rename` |
| Move | `move` on the item, `create`+`upload` on the destination |
| Copy | `read`+`download` on the source, `create`+`upload` on the destination |
| Delete, restore | `delete` |
| Share | `share`, and you cannot grant more than you hold |
| See who has access, revoke a share | `manage` |

Operations that involve two nodes — move, copy — run the check on **both** the
source and the destination. Otherwise a user could move their own file into
someone else's folder, or copy someone else's file out of theirs.

### Structural protections

Some rules are enforced by the schema rather than by code, so no code path can
forget them:

| Rule | Enforced by |
| --- | --- |
| A user has exactly one root folder | Unique partial index, created by a trigger on user insert |
| Two live siblings cannot share a name | Unique partial index on `(parent_id, lower(name))` |
| A folder cannot hold content | `CHECK` constraint |
| The root cannot be renamed, moved, or deleted | Explicit check in the store |
| A folder cannot be moved into its own subtree | Recursive ancestry query inside the transaction |

That last one matters more than it looks: a folder moved inside itself would
detach its entire subtree from the root — unreachable through the interface,
uncounted against quota, and impossible to delete.

---

## 4. The permission model

### Permissions

| Permission | Grants |
| --- | --- |
| `read` | See that the item exists and read its metadata |
| `download` | Retrieve the content |
| `upload` | Add new items to a folder |
| `create` | Create subfolders |
| `modify` | Replace file content |
| `rename` | Change the name |
| `move` | Relocate it |
| `delete` | Move it to Trash |
| `share` | Share it onward with others |
| `manage` | Change who else has access |

`owner` is not a permission but a property of the node, and implies all of them.

### Subjects

A permission is granted to a **user** or to a **group**. A user's effective
permissions are the union of what they hold directly and what their groups hold.

### Inheritance

Permissions inherit down the tree by default. A grant on `Projects` applies to
everything beneath it unless a descendant carries an explicit override.

Evaluation walks from the node toward the root, taking the nearest explicit
grant. A denial always wins over a grant at the same distance, so revoking
access to one subfolder of an otherwise-shared tree is expressible.

### Sharing onward

Sharing is deliberately not transitive by default: holding `read` does not let
you give `read` to somebody else. That requires `share`, granted separately.
Without this rule, one share of one folder eventually reaches everyone.

Nobody may grant a permission they do not hold themselves. A recipient given
read-only access cannot turn round and grant somebody else the right to
delete — otherwise a share would be a privilege-escalation primitive.

### Evaluation

Effective access is the **union** of every applicable grant, minus any denial.
Union rather than nearest-wins because with group membership in play "nearest"
is ambiguous, and a rule nobody can predict is worse than a permissive one.
Denials are the escape hatch, and always win.

Evaluation is one recursive query that walks the ancestry and joins the
applicable grants, so a deeply nested node still costs a single round trip. A
folder listing evaluates all its children in one further query rather than one
per row.

---

## 5. Testing

Permission tests are treated as the most important tests in the codebase. Every
one describes a concrete way one user could reach another's content.

Currently covered, all against a real database and driven over HTTP so the full
middleware chain runs:

- Reading, downloading, listing, renaming, deleting, permanently deleting,
  restoring, and copying another user's node — each must return 404.
- Creating a folder inside another user's folder.
- Uploading into another user's folder.
- Moving or copying one's own file into another user's folder.
- Reading, resuming, finalising, or aborting another user's upload session.
- An administrator attempting to download another user's file.
- Every content route rejecting an anonymous caller.
- A nonexistent identifier and a forbidden one returning byte-identical
  responses.
- Malformed identifiers — including SQL-injection shapes — returning a uniform
  404.
- State-changing routes rejecting a missing or forged CSRF token, and a token
  minted for a different session.
- The `sort` parameter, which reaches an `ORDER BY` clause, refusing to
  interpolate caller text.

Added with the permission model:

- A viewer cannot rename, delete, or share onward.
- A grant of `read` without `download` cannot fetch content.
- A contributor can add but cannot modify, rename, delete, or overwrite.
- A folder share reaches descendants at any depth, including content added
  afterwards; a non-inheriting grant does not.
- Nobody can grant a permission they do not hold.
- An editor cannot revoke somebody else's access.
- A recipient sees only their own entry in the access list.
- Revoking a share, leaving a group, and deleting a group each remove access
  on the very next request.
- An expired share grants nothing.
- Permissions union across two groups.
- A denial overrides an inherited grant, without leaking to siblings.
- The owner keeps control even against a denial aimed at them.
- Breadcrumbs are truncated to what the caller may see, so a shared folder does
  not disclose the owner's private ancestors.
- Group membership is invisible to non-members.
- Creating groups requires an administrator; a group manager can run their own
  team without being one.
