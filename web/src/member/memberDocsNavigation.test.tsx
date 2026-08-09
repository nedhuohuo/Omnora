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
