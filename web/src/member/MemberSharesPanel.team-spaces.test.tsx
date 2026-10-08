import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';

import type { TeamSpaceService } from './teamSpaces';
import type { MemberSpace } from './types';
import {
  addTeamSpaceMember,
  createTeamSpaceFromName,
  TeamSpacesSection,
} from './MemberSharesPanel';

const spaces: MemberSpace[] = [
  { id: 'personal', type: 'personal', name: '我的个人空间', role: 'manager' },
  { id: 'team-manager', type: 'shared', name: '研发资料库', role: 'manager' },
  { id: 'team-editor', type: 'shared', name: '设计协作', role: 'editor' },
];

function teamSpaceService() {
  return {
    createTeamSpace: vi.fn(),
    listTeamSpaceMembers: vi.fn(),
    putTeamSpaceMember: vi.fn(),
    removeTeamSpaceMember: vi.fn(),
  } satisfies TeamSpaceService;
}

describe('MemberSharesPanel team spaces', () => {
  it('keeps the section inside sharing, lists only team spaces, and limits member controls to managers', () => {
    const html = renderToStaticMarkup(
      <TeamSpacesSection
        spaces={spaces}
        service={teamSpaceService()}
        onChanged={() => undefined}
      />,
    );

    expect(html).toContain('团队空间');
    expect(html).toContain('研发资料库');
    expect(html).toContain('设计协作');
    expect(html).not.toContain('我的个人空间');
    expect(html).toContain('成员邮箱或账号 ID');
    expect(html).toContain('添加成员（默认仅查看）');
    expect(html).toContain('管理成员');
  });

  it('uses the injected service and viewer as the default member permission', async () => {
    const service = teamSpaceService();
    service.createTeamSpace.mockResolvedValue({ space: spaces[1], mount: { id: 'mount-1', name: 'Team files', mode: 'read-write' } });
    service.putTeamSpaceMember.mockResolvedValue({ accountId: 'account-2', permission: 'viewer' });

    await createTeamSpaceFromName(service, '  研发资料库  ');
    await addTeamSpaceMember(service, 'team-manager', ' person@example.com ');

    expect(service.createTeamSpace).toHaveBeenCalledWith({ name: '研发资料库' });
    expect(service.putTeamSpaceMember).toHaveBeenCalledWith('team-manager', 'person@example.com', 'viewer');
  });
});
