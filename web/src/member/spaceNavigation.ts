import type { MemberSpace } from './types';

export type MemberSpaceCategory = 'personal' | 'shared';
export type MemberSpaceTab = 'personal-spaces' | 'team-spaces';

export const memberSpaceTabs: readonly MemberSpaceTab[] = ['personal-spaces', 'team-spaces'];

export function categoryForTab(tab: MemberSpaceTab): MemberSpaceCategory {
  return tab === 'personal-spaces' ? 'personal' : 'shared';
}

export function tabForCategory(category: MemberSpaceCategory): MemberSpaceTab {
  return category === 'personal' ? 'personal-spaces' : 'team-spaces';
}

export function categoryForSpace(space: Pick<MemberSpace, 'type'>): MemberSpaceCategory | null {
  if (space.type === 'personal' || space.type === 'shared') return space.type;
  return null;
}

export function spacesForCategory(
  spaces: readonly MemberSpace[],
  category: MemberSpaceCategory,
): MemberSpace[] {
  return spaces.filter((space) => categoryForSpace(space) === category);
}
