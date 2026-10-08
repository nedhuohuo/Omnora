import { describe, expect, it } from 'vitest';
import type { MemberSpace } from './types';
import {
  canManageTeamMembers,
  defaultTeamMemberPermission,
  defaultTeamSpaceService,
  teamSpacesForAccount,
} from './teamSpaces';

const spaces: MemberSpace[] = [
  { id: 'personal', type: 'personal', name: 'Personal', role: 'manager' },
  { id: 'shared-a', type: 'shared', name: 'A', role: 'manager' },
  { id: 'shared-b', type: 'shared', name: 'B', role: 'editor' },
];

describe('team space rules', () => {
  it('keeps only shared spaces for the team-space area', () => {
    expect(teamSpacesForAccount(spaces).map((space) => space.id)).toEqual(['shared-a', 'shared-b']);
  });

  it('allows member management only to a shared-space manager', () => {
    expect(canManageTeamMembers({ id: 'shared-a', type: 'shared', name: 'A', role: 'manager' })).toBe(true);
    expect(canManageTeamMembers({ id: 'shared-b', type: 'shared', name: 'B', role: 'editor' })).toBe(false);
    expect(canManageTeamMembers({ id: 'personal', type: 'personal', name: 'Personal', role: 'manager' })).toBe(false);
  });

  it('defaults a newly added member to viewer access', () => {
    expect(defaultTeamMemberPermission).toBe('viewer');
  });

  it('provides every team-space operation through the default service', () => {
    expect(defaultTeamSpaceService).toEqual(expect.objectContaining({
      createTeamSpace: expect.any(Function),
      listTeamSpaceMembers: expect.any(Function),
      putTeamSpaceMember: expect.any(Function),
      removeTeamSpaceMember: expect.any(Function),
    }));
  });
});
