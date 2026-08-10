import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import {
  AdminSidebarNavigation,
  MemberSidebarNavigation,
  adminTopbarTitle,
  buildAdminNavItems,
  memberTopbarTitle,
  shouldUseAdminShell,
} from './MemberFilesApp';
import { localeMessages } from './i18n';

describe('admin navigation shell', () => {
  it('derives the admin topbar from the active admin tab', () => {
    const text = localeMessages['zh-CN'];
    const adminItems = buildAdminNavItems(text);

    expect(adminTopbarTitle('admin', 'overview', adminItems, text.signIn)).toBe(text.adminOverview);
    expect(adminTopbarTitle('admin', 'mounts', adminItems, text.signIn)).toBe(text.adminStorageSearch);
    expect(adminTopbarTitle('admin', 'route-groups', adminItems, text.signIn)).toBe(text.adminAccessSecurity);
    expect(adminTopbarTitle('admin', 'backups', adminItems, text.signIn)).toBe(text.adminBackups);
    expect(adminTopbarTitle('member', 'route-groups', adminItems, text.signIn)).toBe(text.signIn);
  });

  it('uses admin shell only for admin entry with admin permission', () => {
    expect(shouldUseAdminShell('member', true)).toBe(false);
    expect(shouldUseAdminShell('member', false)).toBe(false);
    expect(shouldUseAdminShell('admin', false)).toBe(false);
    expect(shouldUseAdminShell('admin', true)).toBe(true);
  });

  it('uses member tab labels for the member topbar', () => {
    const text = localeMessages['zh-CN'];

    expect(memberTopbarTitle('personal', text)).toBe('个人空间');
    expect(memberTopbarTitle('team-folders', text)).toBe('团队文件夹');
    expect(memberTopbarTitle('collaborations', text)).toBe('协作');
    expect(memberTopbarTitle('shares', text)).toBe('分享');
    expect(memberTopbarTitle('tokens', text)).toBe('AI Token');
    expect(memberTopbarTitle('docs', text)).toBe('文档');
    expect(memberTopbarTitle('account', text)).toBe('账户');
    expect(memberTopbarTitle('mounts', text)).toBe('个人空间');
  });

  it('keeps admin navigation out of the member sidebar', () => {
    const html = renderToStaticMarkup(
      createElement(MemberSidebarNavigation, { locale: 'zh-CN', activeTab: 'personal', onSelect: () => undefined }),
    );

    expect(html).toContain('个人空间');
    expect(html).toContain('团队文件夹');
    expect(html).toContain('协作');
    expect(html).toContain('分享');
    expect(html).toContain('AI Token');
    expect(html).toContain('文档');
    expect(html).toContain('账户');
    expect(html).not.toContain('账号管理');
    expect(html).not.toContain('存储与搜索');
    expect(html).not.toContain('访问与安全');
    expect(html).not.toContain('备份恢复');
  });

  it('keeps member navigation out of the admin sidebar', () => {
    const text = localeMessages['zh-CN'];
    const html = renderToStaticMarkup(
      createElement(AdminSidebarNavigation, {
        items: buildAdminNavItems(text),
        activeGroupId: 'storage',
        groupTabs: { overview: 'overview', identity: 'users', storage: 'mounts', security: 'route-groups', backups: 'backups' },
        onSelect: () => undefined,
      }),
    );

    expect(html).toContain('概览');
    expect(html).toContain('账号管理');
    expect(html).toContain('存储与搜索');
    expect(html).toContain('访问与安全');
    expect(html).toContain('备份恢复');
    expect(html).not.toContain('个人空间');
    expect(html).not.toContain('团队文件夹');
    expect(html).not.toContain('协作');
    expect(html).not.toContain('分享');
    expect(html).not.toContain('AI Token');
    expect(html).not.toContain('文档');
  });
});
