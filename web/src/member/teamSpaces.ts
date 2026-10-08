import {
  createTeamSpace,
  listTeamSpaceMembers,
  putTeamSpaceMember,
  removeTeamSpaceMember,
} from '../api';
import type { MemberSpace } from './types';

export const defaultTeamMemberPermission = 'viewer' as const;

export const teamSpacesForAccount = (spaces: MemberSpace[]) => spaces.filter((space) => space.type === 'shared');

export const canManageTeamMembers = (space: MemberSpace) => space.type === 'shared' && space.role === 'manager';

export type TeamSpaceService = {
  createTeamSpace: typeof createTeamSpace;
  listTeamSpaceMembers: typeof listTeamSpaceMembers;
  putTeamSpaceMember: typeof putTeamSpaceMember;
  removeTeamSpaceMember: typeof removeTeamSpaceMember;
};

export const defaultTeamSpaceService: TeamSpaceService = {
  createTeamSpace,
  listTeamSpaceMembers,
  putTeamSpaceMember,
  removeTeamSpaceMember,
};
