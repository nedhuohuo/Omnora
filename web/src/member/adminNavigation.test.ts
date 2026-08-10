import { describe, expect, it } from 'vitest';

import { adminTopbarTitle } from './MemberFilesApp';
import { localeMessages } from './i18n';

describe('admin navigation shell', () => {
  it('derives the admin topbar from the active admin tab', () => {
    const text = localeMessages['zh-CN'];
    const adminItems = [
      { id: 'overview' as const, label: text.adminOverview, tabs: [{ id: 'overview' as const, label: text.adminOverview }] },
      { id: 'identity' as const, label: text.adminIdentitySection, tabs: [{ id: 'users' as const, label: text.adminUsers }] },
      { id: 'storage' as const, label: text.adminStorageSearch, tabs: [{ id: 'mounts' as const, label: text.adminMounts }, { id: 'index-jobs' as const, label: text.adminIndexJobs }] },
      { id: 'security' as const, label: text.adminAccessSecurity, tabs: [{ id: 'route-groups' as const, label: text.adminRouteGroups }] },
      { id: 'backups' as const, label: text.adminBackups, tabs: [{ id: 'backups' as const, label: text.adminBackups }] },
    ];

    expect(adminTopbarTitle('admin', 'overview', adminItems, text.signIn)).toBe(text.adminOverview);
    expect(adminTopbarTitle('admin', 'mounts', adminItems, text.signIn)).toBe(text.adminStorageSearch);
    expect(adminTopbarTitle('admin', 'route-groups', adminItems, text.signIn)).toBe(text.adminAccessSecurity);
    expect(adminTopbarTitle('admin', 'backups', adminItems, text.signIn)).toBe(text.adminBackups);
    expect(adminTopbarTitle('member', 'route-groups', adminItems, text.signIn)).toBe(text.signIn);
  });
});
