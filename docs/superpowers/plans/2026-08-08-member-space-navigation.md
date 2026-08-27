# Member Space Navigation Implementation Plan

> **Status: superseded.** The Personal Spaces / Team Spaces IA was rejected. Use [the account, mount, and content authorization design](../specs/2026-08-08-account-mount-access-design.md), member UI option A, and do not execute this plan.

> Historical record only. The remaining steps are non-executable examples of the superseded implementation.

**Goal:** Replace the member “Files” tab with vertical “Personal Spaces” and “Team Spaces” tabs that first show accessible spaces as folder cards, then enter the existing file browser with a category-aware breadcrumb.

**Architecture:** Keep the server API and `MemberSpace` payload unchanged. Add one small pure navigation module for category/type mapping and filtering, plus one presentational space-directory component; `MemberFilesApp` remains the owner of async loading and file-browser state and explicitly clears that state when returning to a category root.

**Tech Stack:** React 19, TypeScript 5.7, Vitest 3, Vite 6, existing Omnora member UI CSS and locale catalog.

**Repository note:** The worktree already contains unrelated changes. Modify only the files named below, preserve every other change, and do not create Git commits unless the user explicitly requests them.

---

## File Map

- Create `web/src/member/spaceNavigation.ts`: authoritative mapping between member space types, category tabs, and filtered space lists.
- Create `web/src/member/spaceNavigation.test.tsx`: pure mapping/filter tests and server-rendered space-directory behavior tests.
- Create `web/src/member/MemberSpaceDirectory.tsx`: folder-card view for one category of accessible spaces.
- Modify `web/src/member/MemberFilesApp.tsx`: replace the Files tab, own category/root/content transitions, and render category-aware breadcrumbs.
- Modify `web/src/member/i18n.ts`: Chinese and English labels, descriptions, roles, and empty states.
- Modify `web/src/member/member-files.css`: vertical-tab compatibility and responsive folder-card layout.

### Task 1: Define and test authoritative space-category mapping

**Files:**
- Create: `web/src/member/spaceNavigation.ts`
- Create: `web/src/member/spaceNavigation.test.tsx`

- [ ] **Step 1: Write the failing category tests**

Create `web/src/member/spaceNavigation.test.tsx` with the mapping tests first:

```tsx
import { describe, expect, it } from 'vitest';

import type { MemberSpace } from './types';
import {
  categoryForSpace,
  categoryForTab,
  spacesForCategory,
  tabForCategory,
} from './spaceNavigation';

const spaces: MemberSpace[] = [
  { id: 'personal-1', type: 'personal', name: 'My Space', role: 'manager' },
  { id: 'shared-1', type: 'shared', name: 'Design Team', role: 'editor' },
  { id: 'future-1', type: 'future', name: 'Unknown Type', role: 'viewer' },
];

describe('member space navigation', () => {
  it('maps vertical tabs to authoritative server space types', () => {
    expect(categoryForTab('personal-spaces')).toBe('personal');
    expect(categoryForTab('team-spaces')).toBe('shared');
    expect(tabForCategory('personal')).toBe('personal-spaces');
    expect(tabForCategory('shared')).toBe('team-spaces');
  });

  it('filters spaces by type without guessing unknown types', () => {
    expect(spacesForCategory(spaces, 'personal').map((space) => space.id)).toEqual(['personal-1']);
    expect(spacesForCategory(spaces, 'shared').map((space) => space.id)).toEqual(['shared-1']);
    expect(categoryForSpace(spaces[2])).toBeNull();
  });
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd web
npm test -- --run src/member/spaceNavigation.test.tsx
```

Expected: FAIL because `./spaceNavigation` does not exist.

- [ ] **Step 3: Implement the minimal pure navigation module**

Create `web/src/member/spaceNavigation.ts`:

```ts
import type { MemberSpace } from './types';

export type MemberSpaceCategory = 'personal' | 'shared';
export type MemberSpaceTab = 'personal-spaces' | 'team-spaces';

export function categoryForTab(tab: MemberSpaceTab): MemberSpaceCategory {
  return tab === 'personal-spaces' ? 'personal' : 'shared';
}

export function tabForCategory(category: MemberSpaceCategory): MemberSpaceTab {
  return category === 'personal' ? 'personal-spaces' : 'team-spaces';
}

export function categoryForSpace(space: Pick<MemberSpace, 'type'>): MemberSpaceCategory | null {
  if (space.type === 'personal' || space.type === 'shared') return space.type;
  return null;
}

export function spacesForCategory(
  spaces: readonly MemberSpace[],
  category: MemberSpaceCategory,
): MemberSpace[] {
  return spaces.filter((space) => categoryForSpace(space) === category);
}
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
cd web
npm test -- --run src/member/spaceNavigation.test.tsx
```

Expected: PASS with 2 tests.

### Task 2: Build the folder-style space directory

**Files:**
- Create: `web/src/member/MemberSpaceDirectory.tsx`
- Modify: `web/src/member/spaceNavigation.test.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`

- [ ] **Step 1: Add failing server-rendered directory tests**

Append these imports and tests to `web/src/member/spaceNavigation.test.tsx`:

```tsx
import { renderToStaticMarkup } from 'react-dom/server';

import MemberSpaceDirectory from './MemberSpaceDirectory';

describe('MemberSpaceDirectory', () => {
  it('renders only the selected category as readable folder cards', () => {
    const html = renderToStaticMarkup(
      <MemberSpaceDirectory
        category="shared"
        locale="zh-CN"
        spaces={spaces}
        onOpen={() => undefined}
      />,
    );

    expect(html).toContain('团队空间');
    expect(html).toContain('Design Team');
    expect(html).toContain('可编辑');
    expect(html).not.toContain('My Space');
    expect(html).not.toContain('shared-1');
    expect(html).not.toContain('Unknown Type');
  });

  it('keeps the category view visible when it has no spaces', () => {
    const html = renderToStaticMarkup(
      <MemberSpaceDirectory
        category="shared"
        locale="zh-CN"
        spaces={spaces.filter((space) => space.type !== 'shared')}
        onOpen={() => undefined}
      />,
    );

    expect(html).toContain('暂无团队空间');
  });
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd web
npm test -- --run src/member/spaceNavigation.test.tsx
```

Expected: FAIL because `MemberSpaceDirectory` and its locale keys do not exist.

- [ ] **Step 3: Add locale keys in both catalogs**

Add the following keys near the existing member navigation labels in `web/src/member/i18n.ts`.

Add these Chinese keys:

```ts
personalSpaces: '个人空间',
teamSpaces: '团队空间',
personalSpacesDetail: '选择一个个人空间以浏览其中的文件',
teamSpacesDetail: '选择一个团队空间以浏览其中的文件',
noPersonalSpaces: '暂无个人空间',
noTeamSpaces: '暂无团队空间',
```

Add these English keys:

```ts
personalSpaces: 'Personal Spaces',
teamSpaces: 'Team Spaces',
personalSpacesDetail: 'Choose a personal space to browse its files',
teamSpacesDetail: 'Choose a team space to browse its files',
noPersonalSpaces: 'No personal spaces',
noTeamSpaces: 'No team spaces',
```

Reuse the existing `spaceRoleViewer`, `spaceRoleEditor`, and `spaceRoleManager` keys from the admin space-membership UI; do not add duplicate object keys or change their current wording.

- [ ] **Step 4: Implement the presentational directory component**

Create `web/src/member/MemberSpaceDirectory.tsx`:

```tsx
import FileTypeIcon from './FileTypeIcon';
import { localeMessages, type MemberLocale } from './i18n';
import { spacesForCategory, type MemberSpaceCategory } from './spaceNavigation';
import type { MemberSpace } from './types';

type MemberSpaceDirectoryProps = {
  category: MemberSpaceCategory;
  locale: MemberLocale;
  spaces: readonly MemberSpace[];
  onOpen: (space: MemberSpace) => void;
};

export default function MemberSpaceDirectory({
  category,
  locale,
  spaces,
  onOpen,
}: MemberSpaceDirectoryProps) {
  const text = localeMessages[locale];
  const visibleSpaces = spacesForCategory(spaces, category);
  const title = category === 'personal' ? text.personalSpaces : text.teamSpaces;
  const detail = category === 'personal' ? text.personalSpacesDetail : text.teamSpacesDetail;
  const empty = category === 'personal' ? text.noPersonalSpaces : text.noTeamSpaces;
  const roleLabels = {
    viewer: text.spaceRoleViewer,
    editor: text.spaceRoleEditor,
    manager: text.spaceRoleManager,
  };

  return (
    <div className="member-page-flow member-space-directory">
      <div className="member-heading">
        <div><h1>{title}</h1><p>{detail}</p></div>
      </div>
      {visibleSpaces.length === 0 ? <div className="member-empty">{empty}</div> : (
        <div className="member-space-grid">
          {visibleSpaces.map((space) => (
            <button className="member-space-card" type="button" onClick={() => onOpen(space)} key={space.id}>
              <FileTypeIcon kind="dir" name={space.name} className="member-file-icon dir" />
              <strong>{space.name}</strong>
              <small>{roleLabels[space.role]}</small>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 5: Add folder-card layout styles**

Add to `web/src/member/member-files.css` beside the existing file-grid styles:

```css
.member-space-directory { max-width: 1120px; }
.member-space-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr)); gap: var(--layout-group); }
.member-space-card { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: var(--space-2) var(--space-3); min-height: 92px; padding: var(--layout-component); border: 1px solid var(--border-card); border-radius: var(--radius-md); background: var(--surface); color: var(--text-body); text-align: left; cursor: pointer; }
.member-space-card:hover { border-color: var(--accent); background: var(--surface-hover); }
.member-space-card .member-file-icon { grid-row: 1 / 3; }
.member-space-card strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-lg); }
.member-space-card small { color: var(--text-faint); font-size: var(--text-sm); }
```

Extend the existing narrow-screen rule with:

```css
@media (max-width: 560px) {
  .member-space-grid { grid-template-columns: 1fr; }
  .member-space-card { min-height: var(--control-lg); }
}
```

- [ ] **Step 6: Run directory and locale tests**

Run:

```bash
cd web
npm test -- --run src/member/spaceNavigation.test.tsx src/member/i18n.test.ts
```

Expected: PASS; the locale parity test confirms the Chinese and English keys remain identical.

### Task 3: Integrate category roots with the existing file browser

**Files:**
- Modify: `web/src/member/MemberFilesApp.tsx`

- [ ] **Step 1: Add navigation imports and replace the member tab type**

Import the new component and navigation helpers:

```tsx
import MemberSpaceDirectory from './MemberSpaceDirectory';
import {
  categoryForSpace,
  categoryForTab,
  tabForCategory,
  type MemberSpaceCategory,
  type MemberSpaceTab,
} from './spaceNavigation';
```

Replace the existing member tab declaration with:

```ts
type MemberTab = MemberSpaceTab | 'trash' | 'shares' | 'tokens' | 'docs' | 'account';
```

Initialize the member entry on its personal-space root:

```ts
const [activeTab, setActiveTab] = useState<MemberTab | AdminTab>(entry === 'admin' ? 'overview' : 'personal-spaces');
```

- [ ] **Step 2: Stop auto-entering the first space**

Change the successful portion of `loadSpaces` to retain a deliberately selected space only while it remains accessible:

```ts
const response = await listSpaces();
setSpaces(response.items);
setActiveSpaceId((current) => response.items.some((space) => space.id === current) ? current : '');
```

Expected behavior: initial load leaves `activeSpaceId` empty and renders the personal-space directory.

- [ ] **Step 3: Add category-root and space-entry transitions**

Replace `selectSpace(spaceId: string)` with these component-local functions:

```ts
function clearSpaceBrowserState() {
  setActiveSpaceId('');
  setMounts([]);
  setActiveMountId('');
  setTrashItems([]);
  setEntries([]);
  setRelativePath('.');
  setSearchQuery('');
  setSearchResults(null);
  setSearchNextCursor('');
  setError('');
}

function openSpaceCategory(category: MemberSpaceCategory) {
  setActiveTab(tabForCategory(category));
  clearSpaceBrowserState();
}

function selectSpace(space: MemberSpace) {
  const category = categoryForSpace(space);
  if (!category) return;
  clearSpaceBrowserState();
  setActiveTab(tabForCategory(category));
  setActiveSpaceId(space.id);
}
```

This is component-local business behavior used by two transitions; do not move it into a generic helper or utility class.

- [ ] **Step 4: Keep trash fallback in the current space category**

Update `selectMount` so unsupported mounts return to the active space’s category file view:

```ts
function selectMount(mount: MemberMount) {
  setActiveMountId(mount.id);
  if (!mountSupportsTrash(mount)) {
    const category = activeSpace ? categoryForSpace(activeSpace) : null;
    if (category) setActiveTab(tabForCategory(category));
    setTrashItems([]);
    setError('');
  }
}
```

Replace the trash effect with this exact fallback so an unsupported mount returns to the current recognized category and an unknown type returns to the personal-space root:

```ts
useEffect(() => {
  if (activeTab !== 'trash') return;
  if (!activeMountSupportsTrash) {
    const category = activeSpace ? categoryForSpace(activeSpace) : null;
    setActiveTab(category ? tabForCategory(category) : 'personal-spaces');
    if (!category) setActiveSpaceId('');
    setTrashItems([]);
    setError('');
    return;
  }
  if (sessionState === 'ready') void refreshTrash();
}, [activeMountSupportsTrash, activeSpace, activeTab, refreshTrash, sessionState]);
```

- [ ] **Step 5: Derive the current category and file/root view**

Near the existing `activeSpace` memo, add:

```ts
const activeSpaceTab = activeTab === 'personal-spaces' || activeTab === 'team-spaces' ? activeTab : null;
const activeCategory = activeSpaceTab
  ? categoryForTab(activeSpaceTab)
  : activeSpace
    ? categoryForSpace(activeSpace)
    : null;
const showingSpaceDirectory = activeSpaceTab !== null && activeSpace === null;
const showingSpaceFiles = activeSpaceTab !== null && activeSpace !== null;
```

Use `showingSpaceFiles` for the top search form so the search box appears only after entering a space.

- [ ] **Step 6: Replace the left member navigation**

Replace the old Files button with two vertical buttons:

```tsx
<button className={`member-nav ${activeTab === 'personal-spaces' ? 'active' : ''}`} type="button" onClick={() => openSpaceCategory('personal')}>{text.personalSpaces}</button>
<button className={`member-nav ${activeTab === 'team-spaces' ? 'active' : ''}`} type="button" onClick={() => openSpaceCategory('shared')}>{text.teamSpaces}</button>
```

Keep the conditional recycle-bin button after these two buttons, followed by Share, AI Token, Docs, and Account. Remove the sidebar section that maps every space to `.member-space` buttons. Render the mount section only when `activeSpace` exists and the active member tab is a space tab or Trash.

- [ ] **Step 7: Render the category directory and category-aware breadcrumb**

Before the existing file-view branch, render the root view:

```tsx
{showingSpaceDirectory && activeCategory ? (
  <MemberSpaceDirectory
    category={activeCategory}
    locale={locale}
    spaces={spaces}
    onOpen={selectSpace}
  />
) : showingSpaceFiles && activeCategory ? (
```

Keep the existing file browser as the second branch. Replace its breadcrumb with:

```tsx
<div className="member-crumbs">
  <button type="button" onClick={() => openSpaceCategory(activeCategory)}>
    {activeCategory === 'personal' ? text.personalSpaces : text.teamSpaces}
  </button>
  <span>
    <b>/</b>
    <button type="button" onClick={() => openDirectory('.')}>{activeSpace.name}</button>
  </span>
  {crumbItems.map((part, index) => (
    <span key={`${part}-${index}`}>
      <b>/</b>
      <button type="button" onClick={() => openDirectory(crumbItems.slice(0, index + 1).join('/'))}>{part}</button>
    </span>
  ))}
</div>
```

Set the file-view heading to `activeSpace.name` when not searching. Keep the remainder of the current file, trash, share, token, docs, account, and admin branches unchanged.

- [ ] **Step 8: Run focused tests and TypeScript production build**

Run:

```bash
cd web
npm test -- --run src/member/spaceNavigation.test.tsx src/member/i18n.test.ts src/member/mountPolicy.test.ts
npm run build
```

Expected: all focused tests PASS; TypeScript emits no errors; Vite produces the production bundle.

### Task 4: Verify the complete member interaction and regression surface

**Files:**
- Verify only; no additional files expected.

- [ ] **Step 1: Run the complete frontend test suite**

Run:

```bash
cd web
npm test -- --run
```

Expected: all Vitest files and tests PASS.

- [ ] **Step 2: Inspect the focused diff for scope and stale Files behavior**

Run:

```bash
git diff -- web/src/member/spaceNavigation.ts web/src/member/spaceNavigation.test.tsx web/src/member/MemberSpaceDirectory.tsx web/src/member/MemberFilesApp.tsx web/src/member/i18n.ts web/src/member/member-files.css
rg -n "activeTab === 'files'|setActiveTab\('files'\)|>\{text\.files\}<" web/src/member/MemberFilesApp.tsx
```

Expected: the diff contains only the planned navigation work; the stale Files-tab search returns no matches.

- [ ] **Step 3: Manually verify the visible member workflow when a browser target is available**

Use a member account that can access at least one `personal` and one `shared` space, then verify:

1. The left navigation begins with “个人空间” and “团队空间”; “文件” is absent.
2. Clicking either category shows only its spaces as folder cards in the content area.
3. Space cards show names and localized roles without internal IDs.
4. Clicking a card enters the existing file browser and loads its mounts/root directory.
5. The breadcrumb category returns to the folder-card directory and hides the recycle-bin entry.
6. Switching categories clears the former path, search, mount, files, trash, and visible error.
7. Empty categories retain their tab and show the category-specific empty state.
8. Share, AI Token, Docs, Account, Trash, language switching, and the separate admin entry still open normally.

Expected: every item matches the confirmed design; no stale content from the prior space remains visible.
