# Omnora Web 全站间距系统 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `web/src/` 全站的页面外边距、区块间距、组件内边距和控件几何统一到可解释、可检查的中等密度设计系统。

**Architecture:** 保留现有 4px 基础间距阶梯，在 `styles.css` 增加页面/组件语义令牌和控件几何令牌；成员端与管理端通过 `member-files.css` 统一标题区、工具栏、表格、卡片、表单和弹窗节奏；公开分享页只使用全局共享样式与自己的 `share-portal.css`，不再导入成员端局部 CSS。业务逻辑、路由和 API 不变。

**Tech Stack:** React 19, TypeScript, Vite, Vitest, 手写 CSS 变量与响应式 media query。

---

## 文件边界

- Create: `web/spacingSystem.test.ts` — 读取 CSS 源文件，验证间距令牌和禁止的页面级调值不会回归。
- Modify: `web/package.json` — 测试基础设施依赖声明。
- Modify: `web/package-lock.json` — 测试基础设施依赖锁定。
- Modify: `web/tsconfig.json` — 将根目录契约测试纳入 TypeScript 检查。
- Modify: `web/src/styles.css` — 基础间距之上的语义布局令牌、控件几何令牌，以及成员端和分享页共同使用的基础 UI 样式。
- Modify: `web/src/member/member-files.css` — 成员端/管理端专属布局与组件样式，迁移到语义令牌并清理局部硬编码值。
- Modify: `web/src/SharePortalApp.tsx` — 移除对成员端局部 CSS 的导入，保持功能和现有 className 语义不变。
- Modify: `web/src/share-portal.css` — 公开分享页独立使用全局令牌和分享页专属规则。
- Modify: `docs/design/ui-design-system.md` — 将已落地的语义令牌、控件高度、响应式规则和例外清单同步为唯一权威规范。
- Reference only: `docs/superpowers/specs/2026-08-07-web-spacing-system-design.md` — 已确认的设计依据，不在实施中改动，除非发现实现级矛盾需要先修正规格。

现有工作区中 `README.md`、`docs/README.md`、`docs/deployment/aliyun-test-server.md`、`docs/mcp/README.md` 和 `docs/deployment/mount-slots.md` 的变更不属于本计划，实施时不得触碰。

### Task 1: 建立可回归的间距契约测试

**Files:**
- Create: `web/spacingSystem.test.ts`
- Modify: `web/package.json`, `web/package-lock.json`, `web/tsconfig.json` — 测试基础设施修改。

- [ ] **Step 1: 写会失败的契约测试**

创建 `web/spacingSystem.test.ts`：

```ts
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

function readCss(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8');
}

describe('web spacing contract', () => {
  it('defines semantic layout and control geometry tokens', () => {
    const css = readCss('./src/styles.css');

    expect(css).toContain('--layout-page-inline: var(--space-7);');
    expect(css).toContain('--layout-page-block-start: var(--space-6);');
    expect(css).toContain('--layout-section-compact: var(--space-5);');
    expect(css).toContain('--layout-component: var(--space-4);');
    expect(css).toContain('--control-md: 36px;');
    expect(css).toContain('--control-lg: 40px;');
    expect(css).toContain('--table-header-height: 40px;');
    expect(css).toContain('--shell-topbar-height: 64px;');
    expect(css).toContain('--layout-content-bottom-reserve: 112px;');
  });

  it('does not retain known page-level spacing hacks', () => {
    const css = readCss('./src/member/member-files.css');

    expect(css).not.toMatch(/\.member-login-panel h1[^}]*margin:\s*4px 0 -12px/);
    expect(css).not.toMatch(/\.member-no-access[^}]*margin:\s*90px auto/);
    expect(css).not.toMatch(/padding:\s*9px 11px/);
    expect(css).not.toMatch(/padding:\s*10px 11px/);
    expect(css).not.toMatch(/gap:\s*2px/);
    expect(css).not.toMatch(/gap:\s*3px/);
    expect(css).not.toMatch(/margin-top:\s*6px/);
  });
});
```

- [ ] **Step 2: 运行测试确认基线失败**

Run: `cd web && npm test -- --run spacingSystem.test.ts`

Expected: FAIL，因为语义令牌尚未存在，且当前成员端 CSS 仍包含登录标题负 margin、无权限页 `90px` margin 和局部 2/3/6/9/11px 调值。

- [ ] **Step 3: 保留测试文件作为后续迁移的回归门槛**

不要为了让测试通过放宽正则；后续每个 CSS 迁移任务完成后，必须重新运行此测试，确保已治理的值不会回归。

### Task 2: 增加基础、语义和控件几何令牌

**Files:**
- Modify: `web/src/styles.css:1-105`

- [ ] **Step 1: 在现有 `--space-*` 阶梯之后增加语义布局令牌**

保留现有值 `4/8/12/16/20/24/32/40px`，紧接着增加：

```css
  --layout-page-inline: var(--space-7);
  --layout-page-block-start: var(--space-6);
  --layout-section: var(--space-6);
  --layout-section-compact: var(--space-5);
  --layout-component: var(--space-4);
  --layout-group: var(--space-3);
  --layout-inline: var(--space-2);
  --layout-micro: var(--space-1);
```

- [ ] **Step 2: 增加控件几何和布局保留令牌**

紧接语义布局令牌增加：

```css
  --control-xs: 28px;
  --control-sm: 32px;
  --control-md: 36px;
  --control-lg: 40px;
  --control-padding-inline: var(--space-3);
  --control-padding-inline-compact: var(--space-2);
  --table-header-height: 40px;
  --table-row-min-height: 52px;
  --shell-topbar-height: 64px;
  --layout-content-bottom-reserve: 112px;
```

- [ ] **Step 3: 把成员端默认顶栏和内容区先接入壳层令牌**

在 `member-files.css` 迁移前，先只替换壳层的值：

```css
.member-topbar {
  height: var(--shell-topbar-height);
}

.member-layout {
  min-height: calc(100vh - var(--shell-topbar-height));
}
```

此时不要改变颜色、列宽或业务结构。

- [ ] **Step 4: 运行契约测试确认令牌部分通过**

Run: `cd web && npm test -- --run spacingSystem.test.ts`

Expected: 第一条 token 测试 PASS；第二条页面 hack 测试仍 FAIL，直到后续任务完成。

### Task 3: 统一成员端页面壳、标题区和页面级 Stack

**Files:**
- Modify: `web/src/member/member-files.css:1-45,100-115`
- Modify: `web/src/member/MemberFilesApp.tsx:970-1041`（为文件/回收站内容增加页面流容器，不改事件和状态逻辑）

- [ ] **Step 1: 迁移成员端内容区和响应式页面边距**

将桌面、平板、手机内容区收敛为：

```css
.member-content {
  padding: var(--layout-page-block-start) var(--layout-page-inline) var(--layout-content-bottom-reserve);
}

@media (max-width: 900px) {
  .member-content {
    padding: var(--space-5) var(--space-6) var(--layout-content-bottom-reserve);
  }
}

@media (max-width: 560px) {
  .member-content {
    padding: var(--space-5) var(--space-4) var(--layout-content-bottom-reserve);
  }
}
```

在 ≤900px 的成员顶栏中保留现有搜索框换行能力，顶栏可以自然增高；将内部 `gap` 统一为 `var(--layout-inline)`，不要使用固定高度裁切第二行。

- [ ] **Step 2: 修正标题区垂直对齐和上下间距**

把现有 `.member-heading` 规则调整为：

```css
.member-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--layout-component);
  margin: var(--layout-group) 0 var(--layout-section-compact);
}

.member-heading h1 {
  margin: 0;
}

.member-heading p {
  margin: var(--layout-inline) 0 0;
  line-height: 1.5;
}

.member-admin-subnav + .member-heading {
  margin-top: 0;
}
```

这样面包屑到标题为 12px，标题到副标题为 8px，标题区到主体为 20px；右侧刷新/创建操作与标题行顶部对齐。

- [ ] **Step 3: 用页面流容器建立文件页和回收站的顺序间距**

在 `MemberFilesApp.tsx` 的 `activeTab === 'files'` 和 `activeTab === 'trash'` 两个 Fragment 外层增加：

```tsx
<div className="member-page-flow">
  {/* 现有 breadcrumbs / heading / toolbar / notices / table 内容保持不变 */}
</div>
```

使用页面流容器而不是负 margin：

```css
.member-page-flow {
  display: grid;
  align-content: start;
}

.member-page-flow > .member-crumbs {
  margin-bottom: var(--layout-group);
}

.member-page-flow > .member-heading {
  margin: 0 0 var(--layout-section-compact);
}

.member-page-flow > .member-toolbar {
  margin: 0 0 var(--layout-component);
}

.member-page-flow > .member-readonly,
.member-page-flow > .member-search-scope,
.member-page-flow > .member-page-error {
  margin: 0 0 var(--layout-group);
}
```

标题区到工具栏保持 20px，工具栏到文件内容保持 16px；所有页面级间距都是正向语义间距，不得新增负 margin。

- [ ] **Step 4: 将管理页第一块主体的上边距交给标题区**

把管理表单和行内表单的外边距改为只保留底部区块间距：

```css
.member-admin-form,
.member-admin-inline-form {
  margin: 0 0 var(--layout-section);
}
```

保证标题区自己的 `20px` 下间距不会和表单 `24px` 上间距叠加。

- [ ] **Step 5: 删除页面级调值**

在同一次修改中移除：

- `.member-login-panel h1` 的 `margin: 4px 0 -12px`，改为 `margin: 0`。
- `.member-no-access` 的 `margin: 90px auto`，改为 `margin: 0 auto`，并用 `min-height: calc(100vh - var(--shell-topbar-height))`、页面内边距和 `align-content: center` 完成居中。
- `.member-admin-subnav` 的 `padding: 3px`，改为 `padding: var(--layout-micro)`。

### Task 4: 统一控件、表格、卡片、表单和弹窗几何

**Files:**
- Modify: `web/src/member/member-files.css:30-39,46-91,100-205`

- [ ] **Step 1: 统一默认按钮、主按钮和输入框高度**

将默认工具栏/次级按钮接入 `--control-md`，表单控件和主操作接入 `--control-lg`，并统一水平内边距：

```css
.member-toolbar button,
.member-secondary-action,
.member-table-action {
  min-height: var(--control-md);
  padding: 0 var(--control-padding-inline);
}

.member-primary,
.member-admin-form-actions button,
.member-admin-inline-form .member-primary {
  min-height: var(--control-lg);
  padding: 0 var(--control-padding-inline);
}

.member-modal input,
.member-login-panel input,
.member-admin-form input,
.member-admin-form select,
.member-admin-inline-form input,
.member-admin-inline-form select {
  height: var(--control-lg);
  padding: 0 var(--control-padding-inline);
}
```

紧凑子导航和小操作使用 `--control-sm`，关闭/复制等符号型操作保留专用的 `--control-xs`，不得用页面间距令牌代替控件高度。

- [ ] **Step 2: 统一文件表格和管理表格**

把两类表格的表头和行改成共享几何：

```css
.member-file-table th,
.member-admin-table th {
  height: var(--table-header-height);
  padding: 0 var(--layout-group);
}

.member-file-table td,
.member-admin-table td {
  height: var(--table-row-min-height);
  padding: var(--layout-inline) var(--layout-group);
}
```

路由表、备份表等包含多行内容的管理表允许内容撑高；不以压缩 padding 解决换行问题。

- [ ] **Step 3: 统一卡片、面板和表单容器内边距**

管理表单、概览卡片、MCP 状态面板、账户区块和弹窗统一使用 `var(--layout-section)` 作为主要内边距；文件网格项作为紧凑内容卡片保留 `var(--layout-component)`。例如：

```css
.member-admin-form,
.member-overview-group,
.member-mcp-status {
  padding: var(--layout-section);
}

.member-file-grid article {
  padding: var(--layout-component);
}

.member-modal {
  gap: var(--layout-component);
  padding: var(--layout-section-compact);
}
```

字段 label 与控件保持 `gap: var(--layout-inline)`，字段组之间使用 `var(--layout-component)`，独立区块之间使用 `var(--layout-section)`。

- [ ] **Step 4: 清理局部 2/3/6/9/10/11px 间距**

按以下固定映射迁移成员端专属组件：

| 当前用途 | 目标 |
| --- | --- |
| 提示条 `9px 11px`、路径建议空态 `10px 11px` | `var(--layout-inline) var(--control-padding-inline)` |
| 分享选择器列表 `gap: 2px`、路径卡片 `gap: 3px` | `var(--layout-micro)` |
| 路由请求提示 `margin-top: 6px` | `var(--layout-micro)` |
| MCP/文档表格 `5px 8px` | `var(--layout-inline) var(--layout-group)` |
| 代码复制按钮 `top/right: 6px` | `var(--layout-inline)` |
| 文件选择器/路径选择器的紧凑操作 | `--control-xs` + `--control-padding-inline-compact` |

不得把所有局部值机械替换成 8px；每一项必须根据表格、提示、控件或内容排版的语义落入对应层级。

- [ ] **Step 5: 为弹窗操作区补充明确 class**

将 `MemberFilesApp.tsx`、`AdminPanels.tsx`、`AdminWorkspace.tsx`、`MemberTokensPanel.tsx`、`MemberSharesPanel.tsx` 和 `MemberAccountPanel.tsx` 中属于弹窗底部操作的匿名 `<div>` 改为：

```tsx
<div className="member-modal-actions">
  <button type="button">{text.cancel}</button>
  <button className="member-primary" type="submit">{text.submit}</button>
</div>
```

保留原有按钮属性、回调和条件渲染；仅增加 class。随后删除 `.member-modal > div:not([class])`，让 `.member-modal-actions` 统一使用 `gap: var(--layout-inline)` 和右对齐。

### Task 5: 将共享基础 UI 与公开分享页解耦

**Files:**
- Modify: `web/src/styles.css:170-230`
- Modify: `web/src/member/member-files.css:1-39`
- Modify: `web/src/SharePortalApp.tsx:17-18`
- Modify: `web/src/share-portal.css:1-25`

- [ ] **Step 1: 识别公开分享页实际依赖的共享 class**

保留公开分享页当前使用的 className，不改业务 JSX；把以下共享规则从 `member-files.css` 提升到全局 `styles.css`，使其不再依赖成员端 CSS 文件：

```text
.member-brand
.member-language
.member-primary
.member-error
.member-readonly
.member-loading
.member-empty
.member-crumbs
.member-file-table
.member-file-name
.member-file-icon
.member-file-actions
.member-preview-error
.member-preview-loading
```

只移动确实被分享页使用的通用规则；`.member-heading`、`.member-toolbar`、侧栏、管理表单、文件网格和成员端弹窗等页面专属规则继续留在 `member-files.css`。

- [ ] **Step 2: 移除公开分享页对成员端 CSS 的导入**

从 `web/src/SharePortalApp.tsx` 删除：

```ts
import './member/member-files.css';
```

保留：

```ts
import './share-portal.css';
```

`main.tsx` 已经全局导入 `styles.css`，因此共享基础规则仍然可用。

- [ ] **Step 3: 迁移分享页壳层与面板间距**

`share-portal.css` 使用以下规则作为基线：

```css
.share-portal-topbar {
  height: var(--shell-topbar-height);
  padding: 0 var(--layout-page-inline);
}

.share-portal-stage {
  min-height: calc(100vh - var(--shell-topbar-height));
  padding: var(--space-8) var(--layout-page-inline);
}

.share-portal-panel {
  padding: var(--layout-section);
}

.share-portal-panel form {
  gap: var(--layout-inline);
  margin-top: var(--layout-component);
}

.share-portal-file-actions {
  gap: var(--layout-inline);
  margin-top: var(--layout-component);
}

.share-portal-md-preview {
  margin-top: var(--layout-component);
}
```

- [ ] **Step 4: 增加分享页平板和手机边距**

```css
@media (max-width: 900px) {
  .share-portal-topbar {
    padding-inline: var(--space-6);
  }

  .share-portal-stage {
    padding: var(--space-6) var(--space-6);
  }
}

@media (max-width: 560px) {
  .share-portal-topbar {
    padding-inline: var(--space-4);
  }

  .share-portal-stage {
    padding: var(--space-6) var(--space-4);
  }
}
```

### Task 6: 同步唯一视觉规范并完成静态契约

**Files:**
- Modify: `docs/design/ui-design-system.md:120-220`
- Modify: `web/spacingSystem.test.ts`

- [ ] **Step 1: 更新规范文档的间距、控件和响应式章节**

将现有规范中的第 4、9、11、12 节同步为实现后的事实：

- 增加 `--layout-*` 和 `--control-*` 令牌表。
- 将默认按钮、表单控件、表头、表格行和顶栏高度改为语义令牌。
- 写明标题区 12/8/20px、工具栏 8/16px、卡片 24px 的层级规则。
- 写明桌面 32/24px、平板 24/20px、手机 16/20px 的响应式边距。
- 记录 `2/3/6/9/11px` 等页面级局部值已禁止，以及控件和内容排版的允许例外。
- 明确成员端窄屏顶栏因搜索框换行允许自然增高。

- [ ] **Step 2: 补充公开分享页 CSS 解耦断言**

在 `spacingSystem.test.ts` 中增加：

```ts
  it('keeps the share portal independent from member page CSS', () => {
    const shareApp = readFileSync(new URL('./SharePortalApp.tsx', import.meta.url), 'utf8');
    const shareCss = readCss('./share-portal.css');

    expect(shareApp).not.toContain("import './member/member-files.css';");
    expect(shareCss).toContain('var(--shell-topbar-height)');
    expect(shareCss).toContain('var(--layout-section)');
  });
```

- [ ] **Step 3: 运行静态契约测试**

Run: `cd web && npm test -- --run spacingSystem.test.ts`

Expected: PASS，包含令牌存在、页面级调值已清理、分享页不再导入成员端 CSS 三组断言。

### Task 7: 工程验证和页面级验收

**Files:**
- Read: all changed files under `web/src/`, `docs/design/ui-design-system.md`

- [ ] **Step 1: 运行前端完整测试**

Run: `cd web && npm test -- --run`

Expected: all existing Vitest tests and `spacingSystem.test.ts` PASS.

- [ ] **Step 2: 运行 TypeScript 和生产构建**

Run: `cd web && npm run build`

Expected: `tsc --noEmit` and Vite production build complete successfully.

- [ ] **Step 3: 检查格式和未授权文件变更**

Run: `git diff --check`

Run: `git status --short`

Expected: no whitespace errors; only this task’s CSS/TSX/test/design-doc changes are present alongside the user’s pre-existing README/deployment changes. Do not stage or commit any file unless the user explicitly asks.

- [ ] **Step 4: 运行本地页面并检查三档视口**

Run: `cd web && npm run dev -- --host 127.0.0.1`

检查宽度：`1440px`、`800px`、`390px`。

检查路由和状态：

```text
/app              文件列表、网格、空状态、工具栏
/admin            概览、用户、空间、挂载、索引、路由组、治理、备份、审计
/share/...        密码页、分享文件、分享目录、Markdown 预览
```

在真实可进入的页面上逐项确认：标题操作区与标题行顶对齐；副标题和主体边框之间有稳定留白；工具栏与表格/空状态之间为 16px；表单卡片、表格行、弹窗和移动端边距没有贴边或重叠。若后端未运行，记录为环境限制，并用静态契约、构建和可用页面完成剩余验证，不把未加载页面误报为通过。

- [ ] **Step 5: 完成前回看设计文档**

逐项对照 `docs/superpowers/specs/2026-08-07-web-spacing-system-design.md` 的目标、响应式规则、例外清单和验收标准；若任一页面需要新增物理值，先把它归入控件几何或内容排版例外，再决定是否同步规范。

## 完成定义

- `web/spacingSystem.test.ts`、现有前端测试、生产构建全部通过。
- 标题区统一解决截图中的副标题/主体边框贴近问题，且右侧操作按钮不再跟副标题底部对齐。
- 成员端、管理端、登录态和公开分享页使用同一套语义间距系统，并保持公开分享页 CSS 独立。
- `docs/design/ui-design-system.md` 与实际代码一致。
- 工作区原有未相关修改被完整保留。
