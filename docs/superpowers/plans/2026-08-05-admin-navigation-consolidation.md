# Admin Navigation Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce the admin sidebar from ten peer entries to five management domains, using second-level tabs only where a domain contains multiple existing admin pages.

**Architecture:** Keep `AdminTab` as the real content routing key so existing panels, API calls, permissions, and destructive confirmations stay unchanged. Add a lightweight grouping layer in `MemberFilesApp.tsx`, render second-level admin tabs above admin content, and add locale/CSS support for the new labels.

**Tech Stack:** React 19, TypeScript, Vite, existing Omnora member/admin CSS.

---

### Task 1: Add admin navigation grouping

**Files:**
- Modify: `web/src/member/MemberFilesApp.tsx`

- [ ] Add `AdminNavGroup` and a default tab map:

```ts
type AdminNavGroup = 'overview' | 'identity-space' | 'storage-search' | 'access-security' | 'backups';

const defaultAdminGroupTabs: Record<AdminNavGroup, AdminTab> = {
  overview: 'overview',
  'identity-space': 'users',
  'storage-search': 'mounts',
  'access-security': 'route-groups',
  backups: 'backups',
};
```

- [ ] Add `adminGroupForTab(tab)` so each existing `AdminTab` belongs to exactly one group.
- [ ] Keep `activeTab` as `MemberTab | AdminTab`, and add remembered per-group admin tab state.
- [ ] Replace the ten admin sidebar buttons with five group buttons.
- [ ] Render second-level tab buttons inside `.member-content` for groups that contain multiple tabs.

### Task 2: Add labels and styling

**Files:**
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`

- [ ] Add Chinese and English labels for `身份与空间`, `存储与搜索`, and `访问与安全`.
- [ ] Add compact second-level tab styles matching the existing toolbar and segmented-control style.
- [ ] Ensure mobile layout scrolls horizontally instead of wrapping into a tall block.

### Task 3: Verify

**Files:**
- Validate: `web/src/member/MemberFilesApp.tsx`
- Validate: `web/src/member/i18n.ts`
- Validate: `web/src/member/member-files.css`

- [ ] Run:

```bash
npm run build
```

Expected: TypeScript and Vite production build pass.
