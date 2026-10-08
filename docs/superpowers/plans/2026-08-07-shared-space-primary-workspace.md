# Shared Space Primary Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote personal and shared spaces to primary member workspaces and support left-sidebar switching across multiple shared spaces.

**Architecture:** Keep the existing `GET /api/v1/spaces` and file APIs. Split the returned spaces by `type`, render personal/shared workspace groups in the sidebar, and reuse the existing file-content state machine when switching the active space.

**Tech Stack:** React, TypeScript, Vitest, existing Go REST API.

---

No commit step is included because this repository owner requires explicit authorization before committing.

### Task 1: Lock the workspace grouping contract

**Files:**
- Modify: `web/src/member/types.ts`
- Create: `web/src/member/spaceNavigation.ts`
- Create: `web/src/member/spaceNavigation.test.ts`

- [ ] Write a failing test proving `personal` spaces and `shared` spaces are returned as separate groups, shared order is stable by name, and unknown types are not promoted into either group.
- [ ] Run `cd web && npm test -- --run src/member/spaceNavigation.test.ts`; expect failure because `groupMemberSpaces` does not exist.
- [ ] Add:

```ts
export function groupMemberSpaces(spaces: MemberSpace[]) {
  return {
    personal: spaces.filter((space) => space.type === 'personal'),
    shared: spaces.filter((space) => space.type === 'shared')
      .sort((left, right) => left.name.localeCompare(right.name)),
  };
}
```

- [ ] Rerun the focused test; expect pass.

### Task 2: Render primary personal/shared workspace navigation

**Files:**
- Modify: `web/src/member/MemberFilesApp.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`
- Create: `web/src/member/MemberWorkspaceNavigation.test.tsx`

- [ ] Write a server-rendered component test asserting the sidebar contains `个人空间`, `共享空间`, two switchable shared-space names, active-state markup, and a visible shared-space empty state.
- [ ] Run the focused test; expect failure because the grouped navigation component is absent.
- [ ] Extract a focused `MemberWorkspaceNavigation` component accepting `spaces`, `activeSpaceId`, `onSelect`, `personalLabel`, `sharedLabel`, and `emptySharedLabel`.
- [ ] Render this component whenever the member is in files/trash mode; do not hide the `共享空间` group when empty.
- [ ] Add bilingual keys `personalWorkspace`, `sharedWorkspace`, and `noSharedSpaces` with key-parity coverage.
- [ ] Add compact sidebar group headings, active rows, overflow handling for long names, and responsive behavior without changing admin navigation.
- [ ] Rerun component and i18n tests; expect pass.

### Task 3: Make space switching reset dependent state

**Files:**
- Modify: `web/src/member/MemberFilesApp.tsx`
- Create: `web/src/member/spaceSelection.ts`
- Create: `web/src/member/spaceSelection.test.ts`

- [ ] Write tests proving a space switch resets relative path to `.`, search results/cursor/query, trash items, preview, pending rename/delete/move/copy state, and selects the first active mount returned for the new space.
- [ ] Run the focused test; expect failure because the transition helper does not exist.
- [ ] Add a pure transition helper describing the state reset and call it from `selectSpace` before loading the new space mounts.
- [ ] Preserve the current space when a refresh returns the same accessible ID; fall back to the personal space, then first shared space, when the active space disappears.
- [ ] Rerun focused tests; expect pass.

### Task 4: Replace the obsolete mock information architecture

**Files:**
- Modify: `web/src/member/ShareCollaborationPreview.tsx`
- Modify: `web/src/member/ShareCollaborationPreview.test.tsx`
- Modify: `web/src/App.tsx`

- [ ] Update the preview test to expect personal/shared workspace groups and multiple left-sidebar shared spaces; remove the expectation that incoming collaboration folders live inside the public-share tab.
- [ ] Run the focused test; expect failure against the old preview.
- [ ] Update `/preview/sharing` to demonstrate the confirmed primary-workspace navigation while keeping public links under the separate public-share entry.
- [ ] Rerun the test and `npm run build`; expect pass with only the existing chunk-size warning.

### Task 5: Update terminology and documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/design/domain-model.md`
- Modify: `docs/design/personal-storage-and-folder-collaboration.md`
- Modify: `docs/api/README.md`

- [ ] Replace “分享与协作” as the primary shared-files entry with “共享空间”.
- [ ] State that mounts are data sources inside spaces, not peer-level spaces.
- [ ] Document left-sidebar switching and shared-space empty behavior.
- [ ] Run `rg -n "分享与协作|挂载空间" README.md docs web/src/member`; inspect every remaining occurrence and keep only public-share or historical-spec references.

### Task 6: Verify the shared workspace slice

**Files:** none

- [ ] Run `cd web && npm test -- --run`; expect all tests pass.
- [ ] Run `cd web && npm run build`; expect TypeScript and Vite build pass.
- [ ] Start `npm run dev -- --host 127.0.0.1`, open the local preview, switch among at least two shared spaces, and verify active state, path reset, and mobile sidebar behavior.
- [ ] Run `git diff --check`; expect no whitespace errors.

