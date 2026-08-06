import type { AppBootstrap } from './types';
import type { MemberDirectoryEntry, MemberMount, MemberSearchResult, MemberSpace } from './member/types';

export type HealthPayload = {
  status?: string;
  ok?: boolean;
  service?: string;
  version?: string;
};

export type ShareFragment = {
  publicId: string;
  secret: string;
};

export type ShareExchangePayload = {
  status?: string;
  shareSessionId?: string;
  publicId?: string;
  expiresAt?: string;
};

export type SessionPayload = {
  userId?: string;
  expiresAt?: string;
  isAdmin?: boolean;
};

export type InitializePayload = {
  token: string;
  email: string;
  displayName: string;
  password: string;
};

export type LoginPayload = {
  login: string;
  password: string;
  totpCode?: string;
};

export type AdminMountPayload = {
  spaceId: string;
  displayName: string;
  rootPath: string;
  kind: 'external' | 'managed';
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
};

export type DirectoryChildrenPayload = {
  relativePath?: string;
  path?: string;
  readOnly?: boolean;
  entries?: MemberDirectoryEntry[];
  items?: MemberDirectoryEntry[];
  nextCursor?: string;
};

export type CreateDirectoryPayload = {
  parentPath: string;
  name: string;
};

export type CreateSharePayload = {
  spaceId: string;
  mountId: string;
  relativePath: string;
  password?: string;
  allowPreview: boolean;
  allowDownload: boolean;
  maxVisits?: number;
  maxDownloads?: number;
  expiresAt?: string;
};

export type CreateShareResponse = {
  id?: string;
  publicId?: string;
  secret?: string;
  fragment?: string;
  fragmentSecret?: string;
  expiresAt?: string;
  share?: unknown;
};

export type CreateAiTokenPayload = {
  name: string;
  scopes: string[];
  boundaries: Array<{
    spaceId: string;
    mountId: string;
    path: string;
  }>;
  expiresAt: string;
};

export type CreateAiTokenResponse = {
  token?: unknown;
  secret?: string;
  bearerToken?: string;
};

export type CreateUploadPayload = {
  spaceId: string;
  mountId: string;
  parentPath: string;
  fileName: string;
  size: number;
};

export type UploadSessionPayload = {
  id?: string;
  expiresAt?: string;
  partSize?: number;
  targetPath?: string;
  expectedSize?: number;
  receivedSize?: number;
  parts?: Array<{ Number?: number; number?: number; Size?: number; size?: number }>;
};

export type TOTPSetupResponse = {
  secret?: string;
  otpauthUri?: string;
};

export type SearchPayload = {
  items?: MemberSearchResult[];
  excludedMounts?: unknown[];
  nextCursor?: string;
};

export type JobPayload = {
  id?: string;
  status?: string;
  kind?: string;
  spaceName?: string;
  mountName?: string;
  priority?: number;
  payload?: Record<string, unknown>;
  checkpoint?: Record<string, unknown>;
  attempts?: number;
  maxAttempts?: number;
  claimedAt?: string;
  claimedBy?: string;
  lastError?: string;
  createdAt?: string;
  updatedAt?: string;
  completedAt?: string;
};

export type AdminSpacePayload = {
  id: string;
  type: string;
  name: string;
};

export type AdminMountListItem = {
  id: string;
  name: string;
  space: string;
  mode: string;
  index: string;
  health: string;
  tone: string;
};

export type AdminRouteGroupItem = {
  id: string;
  label: string;
  exposed: boolean;
  entry: string;
  risk: string;
  tone: string;
};

export type HostDirectoryEntry = {
  name: string;
  path: string;
  kind?: 'external' | 'managed';
};

export type HostDirectoryRoot = {
  path: string;
  kind?: 'external' | 'managed';
};

export type HostDirectorySuggestions = {
  roots?: string[];
  rootDetails?: HostDirectoryRoot[];
  path?: string;
  entries?: HostDirectoryEntry[];
};

export type AuditEventPayload = {
  occurredAt: string;
  actor: string;
  actorEmail?: string;
  actorDisplayName?: string;
  actorLabel?: string;
  routeGroup: string;
  action: string;
  targetType: string;
  targetId: string;
  targetLabel?: string;
  metadata: string;
};

export type AuditEventsPayload = {
  items?: AuditEventPayload[];
};

export type ShareStatus = 'active' | 'expired' | 'revoked';

export type SharePayload = {
  id: string;
  publicId: string;
  fragment?: string;
  spaceId: string;
  spaceName?: string;
  mountId: string;
  mountName?: string;
  relativePath: string;
  creatorEmail?: string;
  creatorDisplayName?: string;
  allowPreview: boolean;
  allowDownload: boolean;
  maxVisits?: number;
  usedVisits: number;
  maxDownloads?: number;
  usedDownloads: number;
  expiresAt: string;
  revokedAt?: string;
  status: ShareStatus;
};

export type AiTokenBoundary = {
  spaceId: string;
  spaceName?: string;
  mountId: string;
  mountName?: string;
  path: string;
};

export type AiTokenStatus = 'active' | 'expired' | 'revoked';

export type AiTokenListItem = {
  id: string;
  publicId?: string;
  accountId?: string;
  accountEmail?: string;
  accountDisplayName?: string;
  name: string;
  scopes: string[];
  boundaries?: AiTokenBoundary[];
  expiresAt?: string;
  createdAt?: string;
  lastUsedAt?: string;
  revokedAt?: string;
  status?: AiTokenStatus;
};

export type ThemePreference = 'system' | 'light' | 'dark';

export type AccountPayload = {
  email: string;
  displayName: string;
  totpEnabled: boolean;
  theme: ThemePreference;
};

export type UpdatePasswordPayload = {
  currentPassword: string;
  newPassword: string;
  revokeTokens?: boolean;
  revokeShares?: boolean;
};

export type AccountSessionPayload = {
  id: string;
  createdAt: string;
  expiresAt: string;
  lastUsedAt?: string;
  current: boolean;
};

export type PreferencesPayload = {
  theme: ThemePreference;
};

export type RenameObjectPayload = {
  from: string;
  toName?: string;
  toPath?: string;
};

export type MoveObjectPayload = {
  from: string;
  toDir: string;
};

export type SharePortalCurrentPayload = {
  path?: string;
  kind?: 'dir' | 'file';
  size?: number;
  modifiedAt?: string;
  previewKind?: string;
  allowPreview?: boolean;
  allowDownload?: boolean;
  expiresAt?: string;
};

export type SharePortalEntry = {
  name: string;
  relativePath: string;
  kind: 'dir' | 'file';
  size?: number;
  modifiedAt?: string;
  readOnly?: boolean;
  previewKind?: string;
};

export type SharePortalChildrenPayload = {
  relativePath?: string;
  readOnly?: boolean;
  entries?: SharePortalEntry[];
};

export type AdminOverviewPayload = {
  accounts?: Record<string, number>;
  spaces?: number;
  mountsByHealth?: Record<string, number>;
  jobsByStatus?: Record<string, number>;
  routeGroups?: AdminRouteGroupItem[];
  latestBackup?: BackupPayload | null;
  risks?: string[];
};

export type AdminUserPayload = {
  id: string;
  email: string;
  displayName: string;
  role: 'admin' | 'member';
  status: string;
  totpRequired: boolean;
  protected: boolean;
};

export type CreateAdminUserPayload = {
  email: string;
  displayName: string;
  password: string;
  role?: 'admin' | 'member';
};

export type CreateAdminUserResponse = {
  user: { id: string; email: string; displayName: string; role: string; status: string };
  space: { id: string; type: string; name: string; role: string };
};

export type CreateAdminSpacePayload = {
  name: string;
};

export type SpaceMemberRole = 'viewer' | 'editor' | 'manager';

export type AdminSpaceMemberPayload = {
  accountId: string;
  email?: string;
  displayName?: string;
  permission: SpaceMemberRole;
  protected?: boolean;
};

export type BackupPayload = {
  id: string;
  status: string;
  path?: string;
  createdBy?: string;
  createdByEmail?: string;
  createdByDisplayName?: string;
  createdByLabel?: string;
  createdAt: string;
  completedAt?: string;
  notes?: string;
};

export type TrashItemPayload = {
  id: string;
  originalPath: string;
  name: string;
  kind: string;
  size: number;
  deletedAt: string;
  trashRelativePath?: string;
};

export class ApiError extends Error {
  readonly status: number;
  readonly body: unknown;

  constructor(message: string, status: number, body: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

async function readJsonResponse(response: Response): Promise<unknown> {
  const text = await response.text();

  if (!text) {
    return undefined;
  }

  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function requestJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');

  if (init.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers,
  });
  const body = await readJsonResponse(response);

  if (!response.ok) {
    throw new ApiError(`Request failed with ${response.status}`, response.status, body);
  }

  return body as T;
}

export function getHealth(signal?: AbortSignal) {
  return requestJson<HealthPayload>('/healthz', { signal });
}

export function getBootstrap(signal?: AbortSignal) {
  return requestJson<Partial<AppBootstrap>>('/api/v1/bootstrap', { signal });
}

export function initialize(payload: InitializePayload, signal?: AbortSignal) {
  return requestJson<unknown>('/api/v1/initialize', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function getSession(signal?: AbortSignal) {
  return requestJson<SessionPayload>('/api/v1/auth/session', { signal });
}

export function login(payload: LoginPayload, signal?: AbortSignal) {
  return requestJson<SessionPayload>('/api/v1/auth/session', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function logout(signal?: AbortSignal) {
  return requestJson<void>('/api/v1/auth/session', {
    method: 'DELETE',
    signal,
  });
}

export function setupTOTP(signal?: AbortSignal) {
  return requestJson<TOTPSetupResponse>('/api/v1/account/totp/setup', {
    method: 'POST',
    signal,
  });
}

export function confirmTOTP(code: string, signal?: AbortSignal) {
  return requestJson<unknown>('/api/v1/account/totp/confirm', {
    method: 'POST',
    body: JSON.stringify({ code }),
    signal,
  });
}

export function registerAdminMount(payload: AdminMountPayload, signal?: AbortSignal) {
  return requestJson<unknown>('/api/v1/admin/mounts', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function renameAdminMount(mountId: string, displayName: string, signal?: AbortSignal) {
  return requestJson<AdminMountListItem>(`/api/v1/admin/mounts/${encodeURIComponent(mountId)}`, {
    method: 'PATCH',
    body: JSON.stringify({ displayName }),
    signal,
  });
}

export function deleteAdminMount(mountId: string, signal?: AbortSignal) {
  return requestJson<{ id?: string; deleted?: boolean }>(
    `/api/v1/admin/mounts/${encodeURIComponent(mountId)}`,
    {
      method: 'DELETE',
      signal,
    },
  );
}

export function reverifyAdminMount(mountId: string, signal?: AbortSignal) {
  return requestJson<AdminMountListItem>(`/api/v1/admin/mounts/${encodeURIComponent(mountId)}/reverify`, {
    method: 'POST',
    signal,
  });
}

export function listAdminSpaces(signal?: AbortSignal) {
  return requestJson<{ items: AdminSpacePayload[] }>('/api/v1/admin/spaces', { signal });
}

export function listAdminMounts(signal?: AbortSignal) {
  return requestJson<{ items: AdminMountListItem[] }>('/api/v1/admin/mounts', { signal });
}

export function listAdminRouteGroups(signal?: AbortSignal) {
  return requestJson<{ items: AdminRouteGroupItem[] }>('/api/v1/admin/route-groups', { signal });
}

export function updateAdminRouteGroup(groupId: string, exposed: boolean, signal?: AbortSignal) {
  return requestJson<AdminRouteGroupItem>(`/api/v1/admin/route-groups/${encodeURIComponent(groupId)}`, {
    method: 'PATCH',
    body: JSON.stringify({ exposed }),
    signal,
  });
}

export function listAdminHostDirectories(path: string, signal?: AbortSignal) {
  const params = new URLSearchParams();
  if (path) {
    params.set('path', path);
  }
  const query = params.toString();
  return requestJson<HostDirectorySuggestions>(`/api/v1/admin/host-directories${query ? `?${query}` : ''}`, { signal });
}

export function listDirectoryChildren(spaceId: string, mountId: string, path: string, signal?: AbortSignal) {
  const params = new URLSearchParams();
  if (path) {
    params.set('path', path);
  }
  const query = params.toString();
  const suffix = query ? `?${query}` : '';

  return requestJson<DirectoryChildrenPayload>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/children${suffix}`,
    { signal },
  );
}

export function listSpaces(signal?: AbortSignal) {
  return requestJson<{ items: MemberSpace[] }>('/api/v1/spaces', { signal });
}

export function listMounts(spaceId: string, signal?: AbortSignal) {
  return requestJson<{ items: MemberMount[] }>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts`, { signal });
}

export function createDirectory(spaceId: string, mountId: string, payload: CreateDirectoryPayload, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/directories`,
    { method: 'POST', body: JSON.stringify(payload), signal },
  );
}

export function downloadURL(spaceId: string, mountId: string, path: string) {
  const params = new URLSearchParams({ path });
  return `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/download?${params.toString()}`;
}

export function previewURL(spaceId: string, mountId: string, path: string) {
  const params = new URLSearchParams({ path, inline: '1' });
  return `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/download?${params.toString()}`;
}

export function searchSpace(spaceId: string, query: string, limit = 50, cursor?: string, signal?: AbortSignal) {
  const params = new URLSearchParams({ q: query, limit: String(limit) });
  if (cursor) {
    params.set('cursor', cursor);
  }
  return requestJson<SearchPayload>(`/api/v1/spaces/${encodeURIComponent(spaceId)}/search?${params.toString()}`, { signal });
}

export async function downloadRange(
  spaceId: string,
  mountId: string,
  path: string,
  rangeHeader: string,
  signal?: AbortSignal,
) {
  const params = new URLSearchParams({ path });
  const headers = new Headers({ Accept: 'application/octet-stream' });
  if (rangeHeader.trim()) {
    headers.set('Range', rangeHeader.trim());
  }
  const response = await fetch(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/download?${params.toString()}`,
    { credentials: 'same-origin', headers, signal },
  );
  const body = await response.text();
  if (!response.ok) {
    let parsed: unknown = body;
    try {
      parsed = JSON.parse(body);
    } catch {
      // Keep the raw body for non-JSON transfer errors.
    }
    throw new ApiError(`Request failed with ${response.status}`, response.status, parsed);
  }
  return {
    status: response.status,
    contentRange: response.headers.get('Content-Range'),
    etag: response.headers.get('ETag'),
    bodyPreview: body.slice(0, 400),
    bytes: body.length,
  };
}

export function createShare(payload: CreateSharePayload, signal?: AbortSignal) {
  return requestJson<CreateShareResponse>('/api/v1/shares', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function createAiToken(payload: CreateAiTokenPayload, signal?: AbortSignal) {
  return requestJson<CreateAiTokenResponse>('/api/v1/ai-tokens', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function listAiTokens(signal?: AbortSignal) {
  return requestJson<{ items?: AiTokenListItem[] }>('/api/v1/ai-tokens', { signal });
}

export function deleteAiToken(tokenId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/ai-tokens/${encodeURIComponent(tokenId)}`, {
    method: 'DELETE',
    signal,
  });
}

export const revokeAiToken = deleteAiToken;

export function createUpload(payload: CreateUploadPayload, signal?: AbortSignal) {
  return requestJson<UploadSessionPayload>('/api/v1/uploads', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function getUpload(uploadId: string, signal?: AbortSignal) {
  return requestJson<UploadSessionPayload>(`/api/v1/uploads/${encodeURIComponent(uploadId)}`, { signal });
}

export async function uploadPart(uploadId: string, partNumber: number, chunk: Blob | string, signal?: AbortSignal) {
  const response = await fetch(`/api/v1/uploads/${encodeURIComponent(uploadId)}/parts/${partNumber}`, {
    method: 'PUT',
    body: chunk,
    credentials: 'same-origin',
    signal,
  });
  const body = await readJsonResponse(response);
  if (!response.ok) {
    throw new ApiError(`Request failed with ${response.status}`, response.status, body);
  }
}

export function completeUpload(uploadId: string, signal?: AbortSignal) {
  return requestJson<unknown>(`/api/v1/uploads/${encodeURIComponent(uploadId)}/complete`, {
    method: 'POST',
    signal,
  });
}

export function cancelUpload(uploadId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/uploads/${encodeURIComponent(uploadId)}`, {
    method: 'DELETE',
    signal,
  });
}

export function listIndexJobs(signal?: AbortSignal) {
  return requestJson<{ items?: JobPayload[] }>('/api/v1/admin/index-jobs', { signal });
}

export function enqueueIndexJob(mountId: string, signal?: AbortSignal) {
  return requestJson<JobPayload>('/api/v1/admin/index-jobs', {
    method: 'POST',
    body: JSON.stringify({ mountId }),
    signal,
  });
}

export function runIndexJob(jobId: string, signal?: AbortSignal) {
  return requestJson<unknown>(`/api/v1/admin/index-jobs/${encodeURIComponent(jobId)}/run`, {
    method: 'POST',
    signal,
  });
}

export function listAuditEvents(limit = 50, signal?: AbortSignal) {
  const params = new URLSearchParams({ limit: String(limit) });
  return requestJson<AuditEventsPayload>(`/api/v1/audit/events?${params.toString()}`, { signal });
}

export function callMCP(bearerToken: string, method: string, params: Record<string, unknown>, signal?: AbortSignal) {
  return requestJson<unknown>('/mcp', {
    method: 'POST',
    headers: { Authorization: `Bearer ${bearerToken}` },
    body: JSON.stringify({ method, params }),
    signal,
  });
}

export function parseShareFragment(hash = window.location.hash): ShareFragment | null {
  const fragment = hash.replace(/^#/, '').trim();

  if (!fragment) {
    return null;
  }

  const separatorIndex = fragment.indexOf('.');

  if (separatorIndex <= 0 || separatorIndex === fragment.length - 1) {
    return null;
  }

  const publicId = decodeURIComponent(fragment.slice(0, separatorIndex));
  const secret = decodeURIComponent(fragment.slice(separatorIndex + 1));

  if (!publicId || !secret) {
    return null;
  }

  return { publicId, secret };
}

export function clearShareFragment() {
  window.history.replaceState(null, document.title, `${window.location.pathname}${window.location.search}`);
}

export async function exchangeShareFragment(fragment: ShareFragment, signal?: AbortSignal) {
  return exchangeShareFragmentWithPassword(fragment, '', signal);
}

export async function exchangeShareFragmentWithPassword(fragment: ShareFragment, password?: string, signal?: AbortSignal) {
  return requestJson<ShareExchangePayload>('/api/v1/share-sessions', {
    method: 'POST',
    body: JSON.stringify({
      public_id: fragment.publicId,
      secret: fragment.secret,
      ...(password ? { password } : {}),
    }),
    signal,
  });
}

// -- Member shares --------------------------------------------------------

export function listShares(signal?: AbortSignal) {
  return requestJson<{ items?: SharePayload[] }>('/api/v1/shares', { signal });
}

export function deleteShare(shareId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/shares/${encodeURIComponent(shareId)}`, {
    method: 'DELETE',
    signal,
  });
}

export function buildShareURL(fragment: string) {
  return `${window.location.origin}/share#${fragment}`;
}

// -- Member account ---------------------------------------------------------

export function getAccount(signal?: AbortSignal) {
  return requestJson<AccountPayload>('/api/v1/account', { signal });
}

export function updateAccountPassword(payload: UpdatePasswordPayload, signal?: AbortSignal) {
  return requestJson<void>('/api/v1/account/password', {
    method: 'PATCH',
    body: JSON.stringify(payload),
    signal,
  });
}

export function listSessions(signal?: AbortSignal) {
  return requestJson<{ items?: AccountSessionPayload[] }>('/api/v1/account/sessions', { signal });
}

export function deleteSession(sessionId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/account/sessions/${encodeURIComponent(sessionId)}`, {
    method: 'DELETE',
    signal,
  });
}

export function disableTOTP(password: string, code: string, signal?: AbortSignal) {
  return requestJson<void>('/api/v1/account/totp/disable', {
    method: 'POST',
    body: JSON.stringify({ password, code }),
    signal,
  });
}

export function getPreferences(signal?: AbortSignal) {
  return requestJson<PreferencesPayload>('/api/v1/account/preferences', { signal });
}

export function updatePreferences(payload: PreferencesPayload, signal?: AbortSignal) {
  return requestJson<PreferencesPayload>('/api/v1/account/preferences', {
    method: 'PUT',
    body: JSON.stringify(payload),
    signal,
  });
}

// -- Member file management --------------------------------------------------

export function renameObject(spaceId: string, mountId: string, payload: RenameObjectPayload, signal?: AbortSignal) {
  return requestJson<unknown>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/rename`,
    { method: 'POST', body: JSON.stringify(payload), signal },
  );
}

export function moveObject(spaceId: string, mountId: string, payload: MoveObjectPayload, signal?: AbortSignal) {
  return requestJson<unknown>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/move`,
    { method: 'POST', body: JSON.stringify(payload), signal },
  );
}

export function deleteObject(spaceId: string, mountId: string, path: string, options?: { permanent?: boolean }, signal?: AbortSignal) {
  const params = new URLSearchParams({ path });
  if (options?.permanent) {
    params.set('permanent', 'true');
  }
  return requestJson<TrashItemPayload | void>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/object?${params.toString()}`,
    { method: 'DELETE', signal },
  );
}

export function listTrash(spaceId: string, mountId: string, signal?: AbortSignal) {
  return requestJson<{ items?: TrashItemPayload[] }>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/trash`,
    { signal },
  );
}

export function emptyTrash(spaceId: string, mountId: string, signal?: AbortSignal) {
  return requestJson<{ removed?: number }>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/trash`,
    { method: 'DELETE', signal },
  );
}

export function restoreTrashItem(spaceId: string, mountId: string, trashId: string, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/trash/${encodeURIComponent(trashId)}/restore`,
    { method: 'POST', signal },
  );
}

export function purgeTrashItem(spaceId: string, mountId: string, trashId: string, signal?: AbortSignal) {
  return requestJson<void>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/trash/${encodeURIComponent(trashId)}`,
    { method: 'DELETE', signal },
  );
}

export function crossMountCopy(
  spaceId: string,
  mountId: string,
  payload: { from: string; toSpaceId: string; toMountId: string; toDir?: string },
  signal?: AbortSignal,
) {
  return requestJson<{ relativePath: string; spaceId: string; mountId: string }>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/cross-mount-copy`,
    {
      method: 'POST',
      body: JSON.stringify(payload),
      signal,
    },
  );
}

export function crossMountMove(
  spaceId: string,
  mountId: string,
  payload: { from: string; toSpaceId: string; toMountId: string; toDir?: string },
  signal?: AbortSignal,
) {
  return requestJson<{ relativePath: string; spaceId: string; mountId: string }>(
    `/api/v1/spaces/${encodeURIComponent(spaceId)}/mounts/${encodeURIComponent(mountId)}/cross-mount-move`,
    {
      method: 'POST',
      body: JSON.stringify(payload),
      signal,
    },
  );
}

// -- Share portal (anonymous, cookie-scoped) ---------------------------------

export function getSharePortalCurrent(signal?: AbortSignal) {
  return requestJson<SharePortalCurrentPayload>('/api/v1/share/current', { signal });
}

export function listSharePortalChildren(path: string, signal?: AbortSignal) {
  const params = new URLSearchParams();
  if (path) {
    params.set('path', path);
  }
  const query = params.toString();
  return requestJson<SharePortalChildrenPayload>(`/api/v1/share/children${query ? `?${query}` : ''}`, { signal });
}

export function sharePortalDownloadURL(path: string, inline = false) {
  const params = new URLSearchParams({ path });
  if (inline) {
    params.set('inline', '1');
  }
  return `/api/v1/share/download?${params.toString()}`;
}

// -- Admin: overview ----------------------------------------------------------

export function getAdminOverview(signal?: AbortSignal) {
  return requestJson<AdminOverviewPayload>('/api/v1/admin/overview', { signal });
}

// -- Admin: users ---------------------------------------------------------------

export function listAdminUsers(signal?: AbortSignal) {
  return requestJson<{ items?: AdminUserPayload[] }>('/api/v1/admin/users', { signal });
}

export function createAdminUser(payload: CreateAdminUserPayload, signal?: AbortSignal) {
  return requestJson<CreateAdminUserResponse>('/api/v1/admin/users', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function disableAdminUser(userId: string, signal?: AbortSignal) {
  return requestJson<{ id: string; status: string }>(`/api/v1/admin/users/${encodeURIComponent(userId)}/disable`, {
    method: 'POST',
    signal,
  });
}

export function enableAdminUser(userId: string, signal?: AbortSignal) {
  return requestJson<{ id: string; status: string }>(`/api/v1/admin/users/${encodeURIComponent(userId)}/enable`, {
    method: 'POST',
    signal,
  });
}

export function revokeAdminUserSessions(userId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/admin/users/${encodeURIComponent(userId)}/revoke-sessions`, {
    method: 'POST',
    signal,
  });
}

// -- Admin: spaces and ACL -------------------------------------------------------

export function createAdminSpace(payload: CreateAdminSpacePayload, signal?: AbortSignal) {
  return requestJson<AdminSpacePayload>('/api/v1/admin/spaces', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function listSpaceMembers(spaceId: string, signal?: AbortSignal) {
  return requestJson<{ items?: AdminSpaceMemberPayload[] }>(
    `/api/v1/admin/spaces/${encodeURIComponent(spaceId)}/members`,
    { signal },
  );
}

export function putSpaceMember(spaceId: string, accountId: string, permission: SpaceMemberRole, signal?: AbortSignal) {
  return requestJson<AdminSpaceMemberPayload>(
    `/api/v1/admin/spaces/${encodeURIComponent(spaceId)}/members/${encodeURIComponent(accountId)}`,
    { method: 'PUT', body: JSON.stringify({ permission }), signal },
  );
}

export function removeSpaceMember(spaceId: string, accountId: string, signal?: AbortSignal) {
  return requestJson<void>(
    `/api/v1/admin/spaces/${encodeURIComponent(spaceId)}/members/${encodeURIComponent(accountId)}`,
    { method: 'DELETE', signal },
  );
}

// -- Admin: share and token governance -------------------------------------------

export function listAdminShares(signal?: AbortSignal) {
  return requestJson<{ items?: SharePayload[] }>('/api/v1/admin/shares', { signal });
}

export function revokeAdminShare(shareId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/admin/shares/${encodeURIComponent(shareId)}`, {
    method: 'DELETE',
    signal,
  });
}

export function listAdminAiTokens(signal?: AbortSignal) {
  return requestJson<{ items?: AiTokenListItem[] }>('/api/v1/admin/ai-tokens', { signal });
}

export function deleteAdminAiToken(tokenId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/admin/ai-tokens/${encodeURIComponent(tokenId)}`, {
    method: 'DELETE',
    signal,
  });
}

export const revokeAdminAiToken = deleteAdminAiToken;

// -- Admin: backups ------------------------------------------------------------------

export function listAdminBackups(signal?: AbortSignal) {
  return requestJson<{ items?: BackupPayload[] }>('/api/v1/admin/backups', { signal });
}

export function createAdminBackup(signal?: AbortSignal) {
  return requestJson<BackupPayload>('/api/v1/admin/backups', {
    method: 'POST',
    signal,
  });
}

export function restoreAdminBackup(backupId: string, confirmPhrase: string, signal?: AbortSignal) {
  return requestJson<{ status: string; backupId: string; notes?: string }>(`/api/v1/admin/backups/${encodeURIComponent(backupId)}/restore`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ confirmPhrase }),
    signal,
  });
}
