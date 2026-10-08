import { describe, expect, it } from 'vitest';

import type { MemberSpace } from './types';
import { orderedMemberSpaceFolders } from './spaceNavigation';

describe('member space navigation', () => {
  it('orders personal and team spaces as content-root folders without producing sidebar groups', () => {
    const spaces: MemberSpace[] = [
      { id: 'shared-zulu', type: 'shared', name: 'Zulu', role: 'editor' },
      { id: 'unknown', type: 'system', name: 'System', role: 'viewer' },
      { id: 'personal', type: 'personal', name: 'Personal', role: 'manager' },
      { id: 'shared-alpha', type: 'shared', name: 'Alpha', role: 'viewer' },
    ];

    const folders = orderedMemberSpaceFolders(spaces);

    expect(folders).toEqual([spaces[2], spaces[3], spaces[0]]);
    expect(folders).not.toContain(spaces[1]);
    expect(folders).not.toHaveProperty('personal');
    expect(folders).not.toHaveProperty('shared');
    expect(spaces.map((space) => space.id)).toEqual(['shared-zulu', 'unknown', 'personal', 'shared-alpha']);
  });

  it('uses ids to stabilize team-space folder order regardless of input order', () => {
    const spaces: MemberSpace[] = [
      { id: 'shared-a', type: 'shared', name: 'Team', role: 'viewer' },
      { id: 'shared-b', type: 'shared', name: 'Team', role: 'viewer' },
    ];

    expect(orderedMemberSpaceFolders(spaces).map((space) => space.id)).toEqual(['shared-a', 'shared-b']);
    expect(orderedMemberSpaceFolders([...spaces].reverse()).map((space) => space.id)).toEqual(['shared-a', 'shared-b']);
  });
});
