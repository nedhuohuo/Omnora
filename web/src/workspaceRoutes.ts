import type { SessionCapabilities } from './api';

export const workspaceRoutePatterns = {
  canonicalAdmin: '/app/admin/:tab?/:resourceId?',
  canonicalMember: '/app/:tab',
  legacyAdmin: '/admin/:tab?/:resourceId?',
} as const;

export const memberTabs = ['files', 'trash', 'shares', 'tokens', 'account'] as const;
export type MemberTab = (typeof memberTabs)[number];

export const adminTabs = [
  'overview',
  'users',
  'spaces',
  'storage',
  'mounts',
  'index-jobs',
  'route-groups',
  'share-governance',
  'token-governance',
  'backups',
  'audit',
] as const;
export type WorkspaceAdminTab = (typeof adminTabs)[number];

export type WorkspaceDestination =
  | { workspace: 'member'; tab: MemberTab }
  | { workspace: 'admin'; tab: WorkspaceAdminTab; resourceId?: string };

export type WorkspaceCapabilities = SessionCapabilities;

type SessionCapabilitySource = {
  isAdmin?: boolean;
  capabilities?: unknown;
};

const memberContentTabs = new Set<MemberTab>(['files', 'trash', 'shares', 'tokens']);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function capabilitiesFromSession(session: SessionCapabilitySource): WorkspaceCapabilities {
  if (session.capabilities === undefined) {
    const isAdmin = session.isAdmin === true;
    return {
      memberWeb: true,
      adminWeb: isAdmin,
      hasContentAccess: true,
      manageSystem: isAdmin,
    };
  }

  const capabilities = isRecord(session.capabilities) ? session.capabilities : {};
  return {
    memberWeb: capabilities.memberWeb === true,
    adminWeb: capabilities.adminWeb === true,
    hasContentAccess: capabilities.hasContentAccess === true,
    manageSystem: capabilities.manageSystem === true,
  };
}

export function isMemberTab(value: string | undefined): value is MemberTab {
  return memberTabs.includes(value as MemberTab);
}

export function isAdminTab(value: string | undefined): value is WorkspaceAdminTab {
  return adminTabs.includes(value as WorkspaceAdminTab);
}

export function workspacePath(destination: WorkspaceDestination): string {
  if (destination.workspace === 'member') return `/app/${destination.tab}`;
  const base = `/app/admin/${destination.tab}`;
  return destination.resourceId ? `${base}/${encodeURIComponent(destination.resourceId)}` : base;
}

export function canAccessWorkspace(destination: WorkspaceDestination, capabilities: WorkspaceCapabilities): boolean {
  if (destination.workspace === 'admin') return capabilities.adminWeb && capabilities.manageSystem;
  if (!capabilities.memberWeb) return false;
  return destination.tab === 'account' || (capabilities.hasContentAccess && memberContentTabs.has(destination.tab));
}

export function defaultWorkspaceDestination(capabilities: WorkspaceCapabilities): WorkspaceDestination | null {
  if (capabilities.memberWeb && capabilities.hasContentAccess) return { workspace: 'member', tab: 'files' };
  if (capabilities.adminWeb && capabilities.manageSystem) return { workspace: 'admin', tab: 'overview' };
  if (capabilities.memberWeb) return { workspace: 'member', tab: 'account' };
  return null;
}

export function visibleWorkspaceSections(capabilities: WorkspaceCapabilities): Array<'member' | 'admin'> {
  const sections: Array<'member' | 'admin'> = [];
  if (capabilities.memberWeb) sections.push('member');
  if (capabilities.adminWeb && capabilities.manageSystem) sections.push('admin');
  return sections;
}
