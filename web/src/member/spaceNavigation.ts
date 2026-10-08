import type { MemberSpace } from './types';

const memberSpaceCollator = new Intl.Collator('en-US');

export function orderedMemberSpaceFolders(spaces: MemberSpace[]): MemberSpace[] {
  return spaces
    .filter((space) => space.type === 'personal' || space.type === 'shared')
    .sort((left, right) => {
      const kindOrder = Number(left.type === 'shared') - Number(right.type === 'shared');
      if (kindOrder) return kindOrder;
      const nameOrder = memberSpaceCollator.compare(left.name, right.name);
      return nameOrder || memberSpaceCollator.compare(left.id, right.id);
    });
}
