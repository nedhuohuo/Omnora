# Initial Admin UI Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the initial-administrator warning badges with ordinary permission and action-cell content while preserving every backend protection.

**Architecture:** Keep the existing `protected` response field as the UI decision input. Change only the two admin-table render branches and remove the now-unused localized badge labels; do not alter API types, server behavior, or shared CSS.

**Tech Stack:** React 19, TypeScript, Vitest, Vite, Go backend regression test

---

### Task 1: Simplify protected administrator table cells

**Files:**
- Modify: `web/src/member/AdminPanels.tsx:321`
- Modify: `web/src/member/AdminPanels.tsx:479-485`
- Modify: `web/src/member/i18n.ts:372-373,782-783`
- Test: `web/src/member/i18n.test.ts`
- Test: `internal/server/admin_control_test.go`

- [ ] **Step 1: Verify the current baseline**

Run:

```bash
npm --prefix web test -- --run
```

Expected: the existing Vitest suite passes before modification.

- [ ] **Step 2: Remove the member-management badge without exposing Disable**

In `AdminUsersPanel`, replace the protected-label branch with a guarded button so a protected active account renders no disable control while the following revoke-sessions button remains unchanged:

```tsx
{!(user.protected && user.status === 'active') && <button className="member-table-action" type="button" onClick={() => void onToggleStatus(user)} disabled={loading}>{user.status === 'active' ? text.userDisable : text.userEnable}</button>}
```

- [ ] **Step 3: Render plain protected space-member values**

In `AdminSpacesPanel`, render the existing administrator column label in the protected permission branch and plain dashes in the protected action branch:

```tsx
{member.protected ? text.userColumnAdmin : <select value={member.permission} onChange={(event) => void onUpdatePermission(member.accountId, event.target.value as SpaceMemberRole)}>
  <option value="viewer">{text.spaceRoleViewer}</option>
  <option value="editor">{text.spaceRoleEditor}</option>
  <option value="manager">{text.spaceRoleManager}</option>
</select>}
```

```tsx
<td>{member.protected ? '--' : <button className="member-table-action member-table-danger" type="button" onClick={() => void onRemoveMember(member.accountId)}>{text.spaceMemberRemove}</button>}</td>
```

- [ ] **Step 4: Remove unused badge translations**

Delete these keys from both `zh-CN` and `en-US` in `web/src/member/i18n.ts`:

```ts
userProtected
spaceMemberProtected
```

Do not rename `spaceRoleManager`; ordinary member dropdowns retain their current wording.

- [ ] **Step 5: Run frontend verification**

Run:

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: all Vitest tests pass, TypeScript reports no errors, and Vite completes a production build.

- [ ] **Step 6: Re-run the backend protection regression**

Run:

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/server -run TestInitialAdminIsProtectedFromAccountAndSpaceMutations -count=1
```

Expected: the targeted Go test passes, proving the server still blocks disabling, permission changes, and space removal for the initial administrator.

- [ ] **Step 7: Review the final diff without committing**

Run:

```bash
git diff --check
git status --short
git diff -- web/src/member/AdminPanels.tsx web/src/member/i18n.ts
```

Expected: no whitespace errors; product-code changes are limited to the approved render branches and unused translations. Leave implementation changes uncommitted unless the user explicitly asks for a commit.
