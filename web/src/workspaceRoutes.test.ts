import { matchRoutes } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import {
  canAccessWorkspace,
  capabilitiesFromSession,
  defaultWorkspaceDestination,
  visibleWorkspaceSections,
  workspacePath,
  workspaceRoutePatterns,
  type WorkspaceCapabilities,
} from './workspaceRoutes';

const fullAccess: WorkspaceCapabilities = {
  memberWeb: true,
  adminWeb: true,
  hasContentAccess: true,
  manageSystem: true,
};

const routeManifest = [
  { id: 'admin', path: workspaceRoutePatterns.canonicalAdmin },
  { id: 'member', path: workspaceRoutePatterns.canonicalMember },
  { id: 'legacy-admin', path: workspaceRoutePatterns.legacyAdmin },
];

describe('workspace routing', () => {
  it('builds canonical member and nested admin locations', () => {
    expect(workspacePath({ workspace: 'member', tab: 'shares' })).toBe('/app/shares');
    expect(workspacePath({ workspace: 'admin', tab: 'storage', resourceId: 'mount / one' }))
      .toBe('/app/admin/storage/mount%20%2F%20one');
  });

  it('matches direct canonical and compatibility URLs', () => {
    const canonical = matchRoutes(routeManifest, '/app/admin/storage/mount-1');
    const canonicalMatch = canonical?.[canonical.length - 1];
    expect(canonicalMatch?.route.id).toBe('admin');
    expect(canonicalMatch?.params).toMatchObject({ tab: 'storage', resourceId: 'mount-1' });

    const legacy = matchRoutes(routeManifest, '/admin/audit');
    const legacyMatch = legacy?.[legacy.length - 1];
    expect(legacyMatch?.route.id).toBe('legacy-admin');
    expect(legacyMatch?.params.tab).toBe('audit');
  });
});

describe('workspace capability guards', () => {
  it('uses the legacy isAdmin fallback only when capabilities are absent', () => {
    expect(capabilitiesFromSession({ isAdmin: true })).toEqual(fullAccess);
    expect(capabilitiesFromSession({ isAdmin: true, capabilities: { memberWeb: true } })).toEqual({
      memberWeb: true,
      adminWeb: false,
      hasContentAccess: false,
      manageSystem: false,
    });
  });

  it('fails closed for null and unknown capability payloads', () => {
    const closedCapabilities = {
      memberWeb: false,
      adminWeb: false,
      hasContentAccess: false,
      manageSystem: false,
    };
    expect(capabilitiesFromSession({ isAdmin: true, capabilities: null })).toEqual(closedCapabilities);
    expect(capabilitiesFromSession({ isAdmin: true, capabilities: ['manageSystem'] })).toEqual(closedCapabilities);
  });

  it('denies direct admin navigation unless both admin capabilities are present', () => {
    const adminRoute = { workspace: 'admin', tab: 'overview' } as const;
    expect(canAccessWorkspace(adminRoute, fullAccess)).toBe(true);
    expect(canAccessWorkspace(adminRoute, { ...fullAccess, adminWeb: false })).toBe(false);
    expect(canAccessWorkspace(adminRoute, { ...fullAccess, manageSystem: false })).toBe(false);
  });

  it('keeps account access but hides content navigation without accessible mounts', () => {
    const noContent = { ...fullAccess, hasContentAccess: false };
    expect(canAccessWorkspace({ workspace: 'member', tab: 'files' }, noContent)).toBe(false);
    expect(canAccessWorkspace({ workspace: 'member', tab: 'account' }, noContent)).toBe(true);
    expect(defaultWorkspaceDestination(noContent)).toEqual({ workspace: 'admin', tab: 'overview' });
    expect(defaultWorkspaceDestination({ ...noContent, adminWeb: false, manageSystem: false }))
      .toEqual({ workspace: 'member', tab: 'account' });
    expect(visibleWorkspaceSections(noContent)).toEqual(['member', 'admin']);
  });

  it('uses each refreshed session response instead of retaining stale admin access', () => {
    const initial = capabilitiesFromSession({ capabilities: fullAccess });
    const refreshed = capabilitiesFromSession({
      capabilities: { ...fullAccess, adminWeb: false, manageSystem: false },
    });
    expect(canAccessWorkspace({ workspace: 'admin', tab: 'users' }, initial)).toBe(true);
    expect(canAccessWorkspace({ workspace: 'admin', tab: 'users' }, refreshed)).toBe(false);
  });
});
