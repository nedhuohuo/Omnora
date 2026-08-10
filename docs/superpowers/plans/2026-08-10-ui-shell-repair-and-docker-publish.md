# UI Shell Repair and Docker Publish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore Omnora's app/admin UI boundaries, sidebar spacing, page padding, and publish the verified Docker image through the existing release path.

**Architecture:** Keep account/session/locale loading shared, but make the rendered shell explicit: member entry renders only member navigation and member panels; admin entry renders only admin navigation and admin panels after admin authorization. Sidebar density is corrected with grouped navigation and token-driven spacing instead of ad hoc CSS.

**Tech Stack:** React 19, TypeScript, Vitest, hand-written CSS design tokens, Vite build, existing Docker/GHCR release workflow.

---

## File Structure

- Modify: `web/src/member/MemberFilesApp.tsx`
  - Owns session flow, entry-specific shell selection, member/admin navigation rendering, and admin access fallback.
- Modify: `web/src/member/member-files.css`
  - Restores token-driven spacing for sidebar groups, top actions, content padding, and mobile behavior.
- Modify: `web/src/member/adminNavigation.test.ts`
  - Covers app/admin shell boundary and admin title behavior with pure render helpers.
- Modify as needed: `web/src/member/contentSourceNavigation.test.tsx`
  - Keeps personal/team/collaboration wording locked.
- Read-only verification: `README.md`, `scripts/verification/release-gate.sh`, `.github/workflows/publish-image.yml`, deploy docs.

## Task 1: Lock the UI Boundary With Tests

- [ ] **Step 1: Add tests proving `/app` never renders admin navigation**

Add exported pure helpers in `MemberFilesApp.tsx` if needed, then assert member navigation excludes admin labels even when `isAdmin` is true.

Run:

```bash
npm test --prefix web -- --run src/member/adminNavigation.test.ts
```

Expected: the new test fails before implementation and passes after Task 2.

- [ ] **Step 2: Add tests proving `/admin` uses admin navigation only**

Assert admin shell labels include grouped admin control-plane labels and exclude member content labels such as personal/team/collaboration.

Run:

```bash
npm test --prefix web -- --run src/member/adminNavigation.test.ts
```

Expected: admin route behavior is explicit and no longer inferred from `isAdmin` inside member entry.

## Task 2: Repair `MemberFilesApp` Shell Selection

- [ ] **Step 1: Gate admin view by entry**

Change admin rendering from `isAdmin && ...` in any entry to `entry === 'admin' && isAdmin`.

- [ ] **Step 2: Render member navigation only in member entry**

Keep `/app` limited to personal space, team folders, collaboration, shares, tokens, docs, and account.

- [ ] **Step 3: Render admin navigation only in admin entry**

Keep `/admin` limited to overview, identity, storage/search, access/security, and backup groups.

- [ ] **Step 4: Add admin no-access state**

If `entry === 'admin'` and the session is ready but `isAdmin` is false, render a focused no-access state with `adminAccessDenied`, `adminAccessDetail`, and a button/link back to `/app`.

Run:

```bash
npm test --prefix web -- --run src/member/adminNavigation.test.ts src/member/contentSourceNavigation.test.tsx
```

Expected: all selected tests pass.

## Task 3: Repair Sidebar Density and Page Rhythm

- [ ] **Step 1: Group member navigation**

Wrap content-source entries and secondary entries in sidebar sections so the left rail is scanable without mixing all tabs at equal weight.

- [ ] **Step 2: Group admin navigation**

Give admin shell a dedicated class hook while reusing design tokens. Do not introduce a new palette or hardcoded spacing.

- [ ] **Step 3: Tighten mobile sidebar behavior**

Ensure grouped sidebars remain horizontally scrollable on small screens without stacked dense buttons.

Run:

```bash
npm run build --prefix web
```

Expected: TypeScript and Vite build pass.

## Task 4: Verify UI and Release Readiness

- [ ] **Step 1: Run targeted tests**

```bash
npm test --prefix web -- --run src/member/adminNavigation.test.ts src/member/contentSourceNavigation.test.tsx
```

- [ ] **Step 2: Run frontend build**

```bash
npm run build --prefix web
```

- [ ] **Step 3: Run repository release gate**

```bash
scripts/verification/release-gate.sh
```

- [ ] **Step 4: Check diff hygiene**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; unrelated untracked files are preserved.

## Task 5: Push Docker Image Through Existing Workflow

- [ ] **Step 1: Identify publish path**

Confirm whether `.github/workflows/publish-image.yml` publishes GHCR on branch push or whether a local `docker buildx build --push` path is documented.

- [ ] **Step 2: Execute the documented publish trigger**

If the project publishes via GitHub workflow, push the verified branch to the expected remote branch after local gates pass. If it publishes locally, run the documented `docker buildx build --push` command with the expected tag.

- [ ] **Step 3: Verify publish result**

Use the existing workflow/log/check command to confirm the Docker publish completed and capture the image tag or digest.

## Self-Review

- Spec coverage: covers UI boundary, admin exposure in member app, sidebar density, page padding, tests, build, release gate, and Docker publish.
- Placeholder scan: no TBD or deferred implementation placeholders.
- Safety: no destructive cleanup, no commit unless explicitly requested, external Docker/GitHub writes require command approval.
