import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import ShareCollaborationPreview, {
  reducePreviewAction,
  selectSpace,
  ShareCollaborationPreviewSidebar,
} from './ShareCollaborationPreview';

describe('ShareCollaborationPreview', () => {
  it('keeps account navigation as one continuous sidebar list', () => {
    const html = renderToStaticMarkup(<ShareCollaborationPreview />);

    expect(html).toContain('文件');
    expect(html).toContain('回收站');
    expect(html).toContain('分享');
    expect(html).toContain('AI Token');
    expect(html).toContain('文档');
    expect(html).toContain('账户');
    expect(html).toMatch(/class="member-nav active"[^>]*aria-current="page"/);
    expect(html).not.toContain('空间选择');
    expect(html).not.toContain('空间类型');
    expect(html).not.toContain('个人空间</button>');
    expect(html).not.toContain('团队空间</button>');
  });

  it('shows personal and team spaces as first-level folders in the file content root', () => {
    const html = renderToStaticMarkup(<ShareCollaborationPreview />);

    expect(html).toContain('空间文件夹列表');
    expect(html).toContain('我的个人空间');
    expect(html).toContain('研发资料库');
    expect(html).toContain('2026 旅行共建');
    expect(html).toContain('3 个空间');
    expect(html).toContain('空间以文件夹形式展示');
    expect(html).toMatch(/data-space-id="personal"/);
    expect(html).toMatch(/data-space-id="shared-research"/);
    expect(html).toMatch(/data-space-id="shared-travel"/);
    expect(html).not.toContain('产品路线图');
    expect(html).not.toContain('设计素材 NAS');
  });

  it('opens a space one level deeper while keeping the main Tab title unchanged', () => {
    const html = renderToStaticMarkup(<ShareCollaborationPreview initialActiveSpaceId="shared-research" />);

    expect(html).toMatch(/<h1[^>]*>文件<\/h1>/);
    expect(html).toContain('研发资料库');
    expect(html).toContain('团队主目录');
    expect(html).toContain('设计素材 NAS');
    expect(html).toContain('产品路线图');
    expect(html).toContain('当前账号为 editor');
    expect(html).not.toContain('空间文件夹列表');
  });

  it('supports the viewer space through the same space-selection reducer', () => {
    let activeSpaceId: string | null = 'shared-research';
    selectSpace((nextSpaceId) => {
      activeSpaceId = typeof nextSpaceId === 'function' ? nextSpaceId(activeSpaceId) : nextSpaceId;
    }, 'shared-travel');

    expect({ activeSpaceId }).toEqual(reducePreviewAction(
      { activeSpaceId: 'shared-research' },
      { type: 'select-space', spaceId: 'shared-travel' },
    ));
    expect(renderToStaticMarkup(<ShareCollaborationPreview initialActiveSpaceId={activeSpaceId} />)).toContain('当前账号为 viewer');
  });

  it('does not add space scope controls to account navigation', () => {
    const sidebar = renderToStaticMarkup(<ShareCollaborationPreviewSidebar activeAccountTab="files" onSelectAccountTab={() => undefined} />);

    expect(sidebar).toContain('文件');
    expect(sidebar).toContain('账户');
    expect(sidebar).not.toContain('MCP 访问');
    expect(sidebar).not.toContain('团队空间');
  });

  it('shows team space management only in the Sharing panel', () => {
    const html = renderToStaticMarkup(<ShareCollaborationPreview initialAccountTab="shares" />);

    expect(html).toContain('团队空间');
    expect(html).toContain('创建团队空间');
    expect(html).toContain('成员与权限');
    expect(html).toContain('可编辑');
    expect(html).toContain('仅查看');
    expect(html).toContain('MCP 空间访问摘要');
    expect(html).toContain('已授权 2 个空间');
  });
});
