import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import type { MemberContentSourcesPayload } from '../api';
import MemberContentSourceDirectory from './MemberContentSourceDirectory';
import MemberCollaborationsDirectory from './MemberCollaborationsDirectory';
import MemberContentNavigation from './MemberContentNavigation';
import { MemberDialogProvider } from './MemberDialog';
import MemberSharesPanel from './MemberSharesPanel';

const sources: MemberContentSourcesPayload = {
  personal: { source: 'personal', label: '我的文件' },
  commonMounts: [
    { source: 'common_mount', mountId: 'common-1', displayName: '设计素材', permission: 'editor', mode: 'read_write' },
  ],
};

describe('member content source directory', () => {
  it('uses file and collaboration navigation without space choices', () => {
    const html = renderToStaticMarkup(
      <MemberContentNavigation locale="zh-CN" active="personal" onSelect={() => undefined} />,
    );

    expect(html).toContain('个人空间');
    expect(html).toContain('团队文件夹');
    expect(html).toContain('协作');
    expect(html).not.toContain('我的文件');
    expect(html).not.toContain('可读写');
  });

  it('shows one direct personal entry and authorized common mount cards', () => {
    const html = renderToStaticMarkup(
      <MemberContentSourceDirectory locale="zh-CN" sources={sources} onOpen={() => undefined} />,
    );

    expect(html).toContain('团队文件夹');
    expect(html).toContain('设计素材');
    expect(html).not.toContain('我的文件');
    expect(html).not.toContain('可读写');
    expect(html).not.toContain('common-1');
  });

  it('never renders the default system mount identity', () => {
    const html = renderToStaticMarkup(
      <MemberContentSourceDirectory locale="zh-CN" sources={sources} onOpen={() => undefined} />,
    );

    expect(html).not.toContain('defaultMountId');
    expect(html).not.toContain('默认挂载');
  });
});

describe('member sharing and collaboration panel', () => {
  it('separates public links from incoming collaborations without Space labels', () => {
    const html = renderToStaticMarkup(<MemberDialogProvider><MemberSharesPanel locale="zh-CN" /></MemberDialogProvider>);
    expect(html).toContain('我的分享');
    expect(html).toContain('共享给我');
    expect(html).toContain('发出的协作');
    expect(html).not.toContain('空间');
    expect(html).not.toContain('spaceId');
  });
});

describe('member collaboration directory', () => {
  it('renders received and sent collaboration sections without spaces', () => {
    const html = renderToStaticMarkup(
      <MemberCollaborationsDirectory locale="zh-CN" incoming={[]} outgoing={[]} onOpen={() => undefined} />,
    );

    expect(html).toContain('收到的协作');
    expect(html).toContain('发出的协作');
    expect(html).toContain('member-collaborations-directory');
    expect(html).toContain('member-section-heading');
    expect(html).not.toContain('Space');
  });
});
