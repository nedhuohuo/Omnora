# 文档中心左侧分类导航 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将成员端文档中心的横向按钮组改为桌面端左侧目录、移动端下拉选择，并保持现有文档内容和主题同步逻辑不变。

**Architecture:** 保留 `MemberDocsPanel` 内部的 `DocsTab` 状态和三个条件渲染分支，将分类控件提取为文档专用导航组件。桌面端渲染语义化 `<nav>`，移动端渲染 `<select>`，两者共享当前分类状态；CSS 使用现有主题变量和响应式断点完成布局切换。

**Tech Stack:** React 19、TypeScript、Vitest、Vite、现有成员端 CSS 变量。

---

## 文件结构与职责

- **Create:** `web/src/member/MemberDocsNavigation.tsx`
  - 只负责文档分类导航的桌面端和移动端渲染。
  - 接收 `locale`、当前 `DocsTab` 和切换回调，不负责文档内容或请求。
- **Create:** `web/src/member/memberDocsNavigation.test.tsx`
  - 使用项目现有 `renderToStaticMarkup` + Vitest 测试导航结构、文案、选中态和移动端控件。
- **Modify:** `web/src/member/MemberDocsPanel.tsx`
  - 导入并渲染导航组件。
  - 保留 `DocsTab` 类型、初始 MCP 状态、Bootstrap 请求、错误处理和正文内容。
- **Modify:** `web/src/member/member-files.css`
  - 增加文档专用两列布局、目录项、移动端下拉控件样式。
  - 删除文档页面对旧 `.member-admin-subnav` 的依赖；其他页面若使用该类则不改动。
- **Modify:** `web/src/member/i18n.ts`
  - 为目录导航增加中英文标签和无障碍名称。

---

### Task 1: 为文档导航补充失败测试和可测试接口

**Files:**
- Create: `web/src/member/memberDocsNavigation.test.tsx`
- Create: `web/src/member/MemberDocsNavigation.tsx`
- Modify: `web/src/member/MemberDocsPanel.tsx`

- [ ] **Step 1: 写失败测试，锁定导航契约**

先创建测试文件。测试先导入尚不存在的导航组件，因此第一次运行应因模块不存在失败。

```tsx
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import MemberDocsNavigation from './MemberDocsNavigation';

describe('member docs navigation', () => {
  it('renders semantic desktop directory and mobile select', () => {
    const html = renderToStaticMarkup(
      <MemberDocsNavigation locale="zh-CN" tab="mcp" onTabChange={vi.fn()} />,
    );

    expect(html).toContain('文档分类');
    expect(html).toContain('MCP 接入');
    expect(html).toContain('REST 契约');
    expect(html).toContain('项目问答');
    expect(html).toContain('<nav');
    expect(html).toContain('<select');
    expect(html).not.toContain('member-admin-subnav');
  });

  it('marks the selected desktop item and select value', () => {
    const html = renderToStaticMarkup(
      <MemberDocsNavigation locale="en-US" tab="openapi" onTabChange={vi.fn()} />,
    );

    expect(html).toContain('REST contract');
    expect(html).toContain('aria-current="page"');
    expect(html).toContain('value="openapi"');
  });
});
```

- [ ] **Step 2: 运行测试确认失败**

Run:

```bash
cd web && npm test -- --run src/member/memberDocsNavigation.test.tsx
```

Expected: FAIL，错误为无法解析 `./MemberDocsNavigation`。

- [ ] **Step 3: 增加导航组件的类型和最小结构**

在 `MemberDocsPanel.tsx` 中将 `DocsTab` 改为导出类型，供导航组件复用：

```tsx
export type DocsTab = 'mcp' | 'openapi' | 'faq';
```

新建 `MemberDocsNavigation.tsx`，使用稳定的导航项数组和同一个回调处理桌面、移动端切换：

```tsx
import type { MemberLocale } from './i18n';
import { localeMessages } from './i18n';
import type { DocsTab } from './MemberDocsPanel';

type LocaleText = (typeof localeMessages)[MemberLocale];

type DocsNavigationProps = {
  locale: MemberLocale;
  tab: DocsTab;
  onTabChange: (tab: DocsTab) => void;
};

const DOCS_NAV_ITEMS: Array<{ tab: DocsTab; label: keyof LocaleText }> = [
  { tab: 'mcp', label: 'docsNavMcp' },
  { tab: 'openapi', label: 'docsNavOpenapi' },
  { tab: 'faq', label: 'docsNavFaq' },
];

export default function MemberDocsNavigation({ locale, tab, onTabChange }: DocsNavigationProps) {
  const text = localeMessages[locale];

  return (
    <div className="member-docs-navigation">
      <nav className="member-docs-directory" aria-label={text.docsNavLabel}>
        {DOCS_NAV_ITEMS.map((item) => (
          <button
            key={item.tab}
            type="button"
            className="member-docs-directory-item"
            aria-current={tab === item.tab ? 'page' : undefined}
            onClick={() => onTabChange(item.tab)}
          >
            {text[item.label]}
          </button>
        ))}
      </nav>
      <label className="member-docs-select-wrap">
        <span>{text.docsNavLabel}</span>
        <select value={tab} onChange={(event) => onTabChange(event.target.value as DocsTab)}>
          {DOCS_NAV_ITEMS.map((item) => <option key={item.tab} value={item.tab}>{text[item.label]}</option>)}
        </select>
      </label>
    </div>
  );
}
```

导航组件只通过 `import type` 引入 `DocsTab`，并在本文件根据 `localeMessages` 推导 `LocaleText`，不增加运行时循环依赖。

- [ ] **Step 4: 增加中英文导航文案**

在 `localeMessages['zh-CN']` 增加：

```ts
docsNavLabel: '文档分类',
docsNavMcp: 'MCP 接入',
docsNavOpenapi: 'REST 契约',
docsNavFaq: '项目问答',
```

在 `localeMessages['en-US']` 增加：

```ts
docsNavLabel: 'Documentation sections',
docsNavMcp: 'MCP integration',
docsNavOpenapi: 'REST contract',
docsNavFaq: 'Project FAQ',
```

由于 `LocaleText` 由两个 locale 的共同结构推断，确保两侧使用完全相同的 key。

- [ ] **Step 5: 运行测试确认导航契约通过**

Run:

```bash
cd web && npm test -- --run src/member/memberDocsNavigation.test.tsx
```

Expected: 2 tests PASS。

---

### Task 2: 将导航接入文档中心并实现桌面/移动布局

**Files:**
- Modify: `web/src/member/MemberDocsPanel.tsx`
- Modify: `web/src/member/member-files.css`

- [ ] **Step 1: 在文档中心接入导航组件**

在 `MemberDocsPanel.tsx` 引入 `MemberDocsNavigation`，删除旧的 `member-admin-subnav` 三个按钮，并把现有错误提示和三个正文分支放入两列布局容器。替换后的返回结构为：

```tsx
      <div className="member-docs-layout">
        <MemberDocsNavigation locale={locale} tab={tab} onTabChange={setTab} />
        <div className="member-docs-content">
          {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

          {tab === 'mcp' && <>
            <p className="member-admin-hint">{text.docsMcpHint}</p>
            <McpDocsBlock endpoint={routeState.endpoint} exposed={routeState.exposed} locale={locale} />
          </>}

          {tab === 'openapi' && <>
            <p className="member-admin-hint">{text.docsOpenapiDetail}</p>
            <p className="member-admin-hint"><code>{text.docsOpenapiBase}</code></p>
            <a className="member-docs-link" href={openapiUrl} target="_blank" rel="noreferrer">{text.docsOpenapiView}</a>
            {OPENAPI_GROUPS.map((group) => (
              <section className="member-docs-section" key={group.title}>
                <h2>{text[group.title]}</h2>
                <table className="member-mcp-docs-table member-docs-table">
                  <thead>
                    <tr><th>{text.docsOpenapiMethod}</th><th>{text.docsOpenapiPath}</th><th>{text.docsOpenapiDesc}</th><th>{text.docsOpenapiAuth}</th></tr>
                  </thead>
                  <tbody>
                    {group.endpoints.map((endpoint) => (
                      <tr key={`${endpoint.method}-${endpoint.path}`}>
                        <td><code>{endpoint.method}</code></td>
                        <td><code>{endpoint.path}</code></td>
                        <td>{text[endpoint.desc]}</td>
                        <td>{authLabel(endpoint.auth, text)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </section>
            ))}
          </>}

          {tab === 'faq' && <>
            <p className="member-admin-hint">{text.docsFaqDetail}</p>
            {FAQ_ITEMS.map((item) => (
              <details className="member-docs-faq" key={item.q}>
                <summary>{text[item.q]}</summary>
                <p>{text[item.a]}</p>
              </details>
            ))}
          </>}
        </div>
      </div>
```

保持三个正文分支内的元素和逻辑不变，只增加 `member-docs-layout` 和 `member-docs-content` 包裹层，使目录与正文成为两列布局的两个子区域。

- [ ] **Step 2: 增加桌面端文档布局样式**

在 `member-files.css` 文档相关样式附近增加：

```css
.member-docs-layout { display: grid; grid-template-columns: 176px minmax(0, 1fr); gap: var(--layout-section-compact); align-items: start; }
.member-docs-navigation { min-width: 0; }
.member-docs-directory { display: grid; gap: var(--space-1); padding: var(--space-2); border: 1px solid var(--border-card); border-radius: var(--radius-md); background: var(--surface-2); }
.member-docs-directory-item { min-height: var(--control-md); padding: 0 var(--control-padding-inline); border: 0; border-left: 3px solid transparent; border-radius: var(--radius-xs); background: transparent; color: var(--text-soft); cursor: pointer; text-align: left; font-size: var(--text-md); }
.member-docs-directory-item:hover { background: var(--surface-hover); color: var(--accent-text); }
.member-docs-directory-item[aria-current="page"] { border-left-color: var(--accent); background: var(--accent-soft); color: var(--accent-text); font-weight: 700; }
.member-docs-select-wrap { display: none; }
.member-docs-content { min-width: 0; }
```

样式必须使用现有主题变量，不能新增固定黑白背景或内联颜色。

- [ ] **Step 3: 增加移动端下拉样式**

在现有 `@media (max-width: 900px)` 规则中增加移动端布局切换：

```css
@media (max-width: 700px) {
  .member-docs-layout { display: block; }
  .member-docs-directory { display: none; }
  .member-docs-select-wrap { display: grid; gap: var(--space-1); margin-bottom: var(--layout-component); color: var(--text-muted); font-size: var(--text-sm); }
  .member-docs-select-wrap select { width: 100%; min-height: var(--control-lg); padding: 0 var(--control-padding-inline); border: 1px solid var(--border-strong); border-radius: var(--radius-sm); background: var(--input-bg); color: var(--text-body); font-size: var(--text-md); }
}
```

在窄屏下确保 `.member-docs-content` 不设置固定宽度，避免 OpenAPI 表格通过外层布局造成页面横向溢出；表格已有的溢出处理保持不变。

- [ ] **Step 4: 运行 TypeScript 检查与导航测试**

Run:

```bash
cd web && npm test -- --run src/member/memberDocsNavigation.test.tsx && npx tsc --noEmit
```

Expected: 导航测试通过，TypeScript 无错误。

---

### Task 3: 完善回归测试并验证主题与旧 API 隔离

**Files:**
- Modify: `web/src/member/memberDocsNavigation.test.tsx`

- [ ] **Step 1: 增加默认页面和旧分类控件回归断言**

在测试中渲染完整 `MemberDocsPanel` 的静态 HTML，验证默认 MCP 内容、三个新分类名称和旧分类类名不存在：

```tsx
import MemberDocsPanel from './MemberDocsPanel';

it('keeps MCP as the default docs section without the legacy segmented control', () => {
  const html = renderToStaticMarkup(<MemberDocsPanel locale="zh-CN" />);

  expect(html).toContain('MCP 标准服务');
  expect(html).toContain('MCP 接入');
  expect(html).toContain('REST 契约');
  expect(html).toContain('项目问答');
  expect(html).not.toContain('member-admin-subnav');
});
```

不在该测试中模拟浏览器点击；项目当前测试环境使用服务端静态渲染，导航组件测试验证选中态属性和 `onTabChange` 的绑定结构，TypeScript 检查验证回调的 `DocsTab` 类型边界。

- [ ] **Step 2: 检查文档组件未恢复废弃 Space 请求**

Run:

```bash
rg -n "listSpaces|member-admin-subnav" web/src/member/MemberDocsPanel.tsx web/src/member/MemberDocsNavigation.tsx web/src/member/memberDocsNavigation.test.tsx
```

Expected: 无输出。OpenAPI 参考表中的历史契约路径不属于本次导航组件请求代码，不修改其既有内容，避免扩大范围。

- [ ] **Step 3: 运行完整前端验证**

Run:

```bash
(cd web && npm test -- --run && npm run build)
git diff --check
```

Expected:

- Vitest 全部通过。
- Vite build 成功。
- `git diff --check` 无输出。

- [ ] **Step 4: 检查最终差异和本地页面**

Run:

```bash
git diff --stat
git status --short
```

使用当前本地前端 `http://localhost:5173` 检查：

- 桌面端文档页显示左侧目录和右侧正文。
- 切换三个目录项均显示对应内容。
- 缩窄到移动端宽度后显示下拉框，不显示左侧目录。
- 白色和深色主题下目录当前项都可读。

- [ ] **Step 5: 按功能提交实现**

```bash
git add web/src/member/MemberDocsNavigation.tsx web/src/member/memberDocsNavigation.test.tsx web/src/member/MemberDocsPanel.tsx web/src/member/member-files.css web/src/member/i18n.ts
git commit -m "feat: 优化文档中心分类导航"
```
