# Sharing

Internal sharing gives another user, or a group, access to a file or folder you
control. Public links — access without an account — arrive in Phase 6 and are
documented separately.

---

## 1. What a share is

A share **is** a permission grant. There is no separate "shares" table: the row
the sharing dialog creates is the same row the access evaluator reads.

That is deliberate. Two records for one question is two sources of truth, and
one day they disagree — somebody keeps access they were told had been revoked,
and nothing in the interface shows why. One row means revoking a share and
losing access are the same event.

The `/api/v1/shares` endpoints are the human-facing view of the `permissions`
table.

---

## 2. Roles

The dialog offers four roles rather than ten checkboxes. Each is a preset over
the same underlying permissions, and the interface fetches the list from the
server so it can never offer a role the server does not recognise.

| Role | Can | Cannot |
| --- | --- | --- |
| **Viewer** | Open and download | Change anything |
| **Contributor** | Add new files and folders | Change or remove what is already there |
| **Editor** | Add, change, and remove content | Share it with anyone else |
| **Manager** | Everything an editor can, plus share it and manage who has access | Take ownership |

Underneath, a grant is a set of these permissions:

`read` · `download` · `upload` · `create` · `modify` · `rename` · `move` ·
`delete` · `share` · `manage`

The API accepts either a role name or an explicit list, so an unusual
arrangement is expressible without a new concept.

### Why contributor exists

A contributor can put files into a folder but cannot see them changed or
removed afterwards — the "drop box" arrangement. It is what you want for
collecting submissions: everyone can add, nobody can tamper with what somebody
else submitted.

### Why sharing is not transitive

Holding `read` does not let you give `read` to somebody else. That requires
`share`, which only the manager role includes by default.

Without this rule, one share of one folder eventually reaches everyone: each
recipient passes it on, and the owner has no way to see or stop it. Making
onward sharing a separate, deliberately granted permission keeps the owner in
control of how far something travels.

---

## 3. Inheritance

A grant on a folder reaches everything inside it, at any depth, including
content added *after* the share was created. That is what makes sharing a
project folder useful rather than a chore.

A grant can be pinned to exactly one node by setting `inherit: false`.

### Denials

A denial removes permissions and always wins, at any depth. It is what lets you
share a whole tree and carve one subfolder back out:

```text
Projects/            shared with Bob as editor
├── Public/            → Bob is an editor here
├── Drafts/            → and here
└── Private/         denied to Bob
    └── salary.pdf     → Bob cannot see this, or that it exists
```

Denials are an administrator-level construct and are not exposed in the sharing
dialog in this release; they are created through the API.

### How effective access is computed

1. **The owner may do everything.** Ownership is not a grant and cannot be
   revoked by one — not even by a denial aimed at the owner.
2. Otherwise, collect every grant that applies: on the node itself, and on any
   inheritable ancestor.
3. Grants apply to the user directly *and* through every group they belong to.
   Effective access is the **union**.
4. Any denial removes its permissions.

Union rather than "nearest wins" is deliberate. With group membership in play,
"nearest" is ambiguous — two grants at the same depth from different groups
have no natural precedence — and a rule nobody can predict is worse than a
permissive one. Denials are the escape hatch.

---

## 4. Groups

A group is a named set of users, so a folder can be shared with "Accounting"
rather than with eleven people individually.

The property that makes groups worth having: **adding somebody to the group
gives them the group's access immediately**, across every share, without
revisiting any of them. Removing them takes it away just as immediately.

| Action | Who can do it |
| --- | --- |
| Search groups by name | Any signed-in user — sharing requires finding one |
| See a group's members | Members of that group, and administrators |
| Add or remove members | Administrators, and managers of that specific group |
| Create, rename, delete a group | Administrators only |
| Leave a group | Anyone, for themselves |

Group membership is **not public**. Who works with whom is not something a
shared server should broadcast, so a non-member asking about a group gets the
same 404 as for a group that does not exist. Searching by name returns only
names, descriptions, and member counts.

Deleting a group removes its grants, enforced by a database trigger rather than
application code: access must never outlive the group it was given to.

---

## 5. Ownership of shared content

Content created inside somebody else's shared folder belongs to **the folder's
owner**, not to whoever created it, and counts against their quota.

This follows from what a shared folder is. If a contributor's uploads belonged
to the contributor, the folder's owner could not manage their own tree, could
not delete what was put there, and the storage would be charged to somebody who
never agreed to host it. Copies made into a shared folder work the same way.

---

## 6. What you cannot do

**Share your root folder.** It would hand over your entire account. Share a
folder inside it instead.

**Grant more than you hold.** A recipient with read-only access cannot turn
round and grant somebody else the right to delete. Without this rule, sharing
would be a privilege-escalation primitive.

**Share something you cannot see.** Attempting it returns the same 404 as for a
file that does not exist, so the endpoint cannot be used to probe for content.

**See who else has access, without `manage`.** A recipient sees only their own
entry. Otherwise every share would leak the list of everyone else with access.

---

## 7. Expiry

A share can carry an expiry. Once it passes, the grant stops applying
immediately — evaluation ignores expired rows rather than waiting for a cleanup
job — and the row is removed later by the sweeper.

An expiry in the past is rejected at creation.

---

## 8. Administrators

An administrator manages accounts, quotas, groups, and the server itself.

**An administrator does not get blanket access to other people's files.**
Administering a server is not the same as being entitled to read what people
store on it, and an audit log would have no way to distinguish legitimate
support from snooping. This is tested: an administrator attempting to download
another user's unshared file receives a 404, identical to any other user.

An administrator who genuinely needs a user's content has the same options
anyone else does: ask them to share it, or restore it from a backup — both of
which leave a trace.

---

## 9. What the interface shows

Breadcrumbs are truncated to the ancestors you can actually see. Opening a
shared folder shows the path from the shared folder down, not from the owner's
root — which would both disclose folder names you have no access to and imply
the item lives in your own storage.

Menu items for actions you lack permission for are disabled. That is a
usability affordance and nothing more: every endpoint re-evaluates permissions
on every request, so forcing a hidden action gains nothing.

---

## 10. Not yet implemented

| | Phase |
| --- | --- |
| Public links, password-protected links, upload requests | 6 |
| Denials in the sharing dialog | After v1 |
| Notifying a recipient that something was shared | Needs outbound email; not scheduled for v1 |
| Transferring ownership | Under consideration |
