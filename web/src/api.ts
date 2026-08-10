type AppBootstrap = {
  initializationAvailable?: boolean;
  routeGroups?: Array<{
    id: string;
    exposed: boolean;
    entry?: string;
    risk?: string;
    tone?: string;
  }>;
};
import type { MemberDirectoryEntry, MemberSearchResult } from './member/types';

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
  isInitialAdmin?: boolean;
  purpose?: 'full' | 'totp_enrollment';
  requiresTotpEnrollment?: boolean;
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

export type ReauthenticatePayload = {
  password?: string;
  totpCode?: string;
};

export type ReauthenticateResponse = {
  status?: 'reauthenticated';
  reauthenticatedUntil?: string;
};

export type MountGovernance = 'normal' | 'restricted';
export type MountGrantPermission = 'viewer' | 'editor';

export type AdminMountGrantInput = {
  accountId: string;
  permission: MountGrantPermission;
};

export type AdminMountPayload = {
  displayName: string;
  rootPath: string;
  governance: MountGovernance;
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
  grants: AdminMountGrantInput[];
};

export type DirectoryChildrenPayload = {
  relativePath?: string;
  path?: string;
  readOnly?: boolean;
  entries?: MemberDirectoryEntry[];
  items?: MemberDirectoryEntry[];
  nextCursor?: string;
};

export type PersonalContentLocator = {
  source: 'personal';
  path: string;
};

export type CommonMountContentLocator = {
  source: 'common_mount';
  mountId: string;
  path: string;
};

export type CollaborationContentLocator = {
  source: 'collaboration';
  collaborationId: string;
  path: string;
};

export type MemberContentLocator =
  | PersonalContentLocator
  | CommonMountContentLocator
  | CollaborationContentLocator;

export type MemberContentSourceLocator =
  | { source: 'personal' }
  | { source: 'common_mount'; mountId: string }
  | { source: 'collaboration'; collaborationId: string };

export type MemberContentSourcesPayload = {
  personal: {
    source: 'personal';
    label: string;
  };
  commonMounts: Array<{
    source: 'common_mount';
    mountId: string;
    displayName: string;
    permission: 'viewer' | 'editor';
    mode: 'read_only' | 'read_write';
  }>;
};

export type MemberCollaboration = {
  id: string;
  source?: 'collaboration';
  collaborationId?: string;
  displayName?: string;
  folderName?: string;
  rootRelativePath?: string;
  path?: string;
  ownerName?: string;
  recipientName?: string;
  permission: 'viewer' | 'editor';
  status?: string;
};

export type MemberCollaborationsPayload = {
  items: MemberCollaboration[];
};

export type CreateSharePayload = {
  source: 'personal' | 'common_mount' | 'collaboration';
  mountId?: string;
  collaborationId?: string;
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

export type AiTokenScope =
  | 'mounts:read'
  | 'files:list'
  | 'files:metadata'
  | 'files:text'
  | 'files:download_ticket'
  | 'search:read'
  | 'uploads:create'
  | 'files:write'
  | 'files:trash'
  | 'trash:read'
  | 'files:restore'
  | 'files:purge'
  | 'shares:read'
  | 'shares:create'
  | 'shares:revoke';

export type CreateAiTokenPayload = {
  name: string;
  scopes: AiTokenScope[];
  boundaries: AiTokenBoundary[];
  /** Omit for a token that never expires. */
  expiresAt?: string;
};

export type CreateAiTokenResponse = {
  token: {
    id: string;
    publicId: string;
    name: string;
    scopes: AiTokenScope[];
    expiresAt: string;
  };
  secret: string;
  bearerToken: string;
};

export type CreateUploadPayload = {
  source: 'personal' | 'common_mount' | 'collaboration';
  mountId?: string;
  collaborationId?: string;
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

export type AdminMountGrant = AdminMountGrantInput & {
  email: string;
  displayName: string;
};

export type AdminMountListItem = {
  id: string;
  displayName: string;
  rootPath: string;
  governance: MountGovernance;
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
  shareEnabled: boolean;
  status: 'pending' | 'active' | 'disabled' | 'unavailable';
  grantCount: number;
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
  source: 'personal' | 'common_mount';
  mountId?: string;
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

export type AiTokenBoundary =
  | {
    source: 'all_account_content';
    mountId?: never;
    path?: never;
  }
  | {
    source: 'personal';
    mountId?: never;
    path?: string;
  }
  | {
    source: 'common_mount';
    mountId: string;
    mountName?: string;
    path?: string;
  };

export type AiTokenStatus = 'active' | 'expired' | 'revoked';

export type AiTokenListItem = {
  id: string;
  publicId?: string;
  accountId?: string;
  accountEmail?: string;
  accountDisplayName?: string;
  name: string;
  scopes: AiTokenScope[];
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
  passwordResetRecommended?: boolean;
  theme: ThemePreference;
};

export type UpdatePasswordPayload = {
  currentPassword: string;
  newPassword: string;
  totpCode?: string;
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
  commonMounts?: number;
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

export type RecoveryControlPayload = {
  state?: string;
  ready?: boolean;
  requestId?: string;
  reasonCode?: string;
  cleanupPending?: boolean;
  sourceSchemaVersion?: number | null;
  requestedAt?: string;
  completedAt?: string;
  updatedAt?: string;
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

export type APIAuthContext = 'account' | 'share';

export type APIRequestInit = RequestInit & {
  authContext?: APIAuthContext;
};

export function isReauthenticationRequired(error: unknown): boolean {
  if (!(error instanceof ApiError) || error.status !== 403) {
    return false;
  }
  const body = error.body as { error?: { code?: string } } | undefined;
  return body?.error?.code === 'reauthentication_required';
}

export class ReauthenticationCanceledError extends Error {
  constructor() {
    super('reauthentication canceled');
    this.name = 'ReauthenticationCanceledError';
  }
}

export function isReauthenticationCanceled(error: unknown): boolean {
  return error instanceof ReauthenticationCanceledError;
}

export async function requestJson<T>(path: string, init: APIRequestInit = {}): Promise<T> {
  const { authContext = 'account', ...requestInit } = init;
  const headers = new Headers(requestInit.headers);
  headers.set('Accept', 'application/json');

  const method = (requestInit.method ?? 'GET').toUpperCase();
  if (!['GET', 'HEAD', 'OPTIONS', 'TRACE'].includes(method)) {
    const csrf = readCsrfCookie(authContext);
    if (csrf) {
      headers.set('X-CSRF-Token', csrf);
    }
  }

  if (requestInit.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  const response = await fetch(path, {
    ...requestInit,
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

export function reauthenticate(payload: ReauthenticatePayload, signal?: AbortSignal) {
  return requestJson<ReauthenticateResponse>('/api/v1/account/reauthenticate', {
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
  return requestJson<AdminMountListItem>('/api/v1/admin/mounts', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function updateAdminMount(mountId: string, payload: {
  displayName?: string;
  mode?: 'read_only' | 'read_write';
  indexEnabled?: boolean;
  shareEnabled?: boolean;
}, signal?: AbortSignal) {
  return requestJson<AdminMountListItem>(`/api/v1/admin/mounts/${encodeURIComponent(mountId)}`, {
    method: 'PATCH',
    body: JSON.stringify(payload),
    signal,
  });
}

export function renameAdminMount(mountId: string, displayName: string, signal?: AbortSignal) {
  return updateAdminMount(mountId, { displayName }, signal);
}

export function deleteAdminMount(mountId: string, displayName: string, signal?: AbortSignal) {
  return requestJson<{ id: string; deleted: boolean; deleteData: boolean; dataDeleted: boolean }>(
    `/api/v1/admin/mounts/${encodeURIComponent(mountId)}`,
    {
      method: 'DELETE',
      body: JSON.stringify({ displayName }),
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

export function listAdminMounts(signal?: AbortSignal) {
  return requestJson<{ items: AdminMountListItem[] }>('/api/v1/admin/mounts', { signal });
}

export function listAdminMountGrants(mountId: string, signal?: AbortSignal) {
  return requestJson<{ items: AdminMountGrant[] }>(`/api/v1/admin/mounts/${encodeURIComponent(mountId)}/grants`, { signal });
}

export function putAdminMountGrant(mountId: string, accountId: string, permission: MountGrantPermission, signal?: AbortSignal) {
  return requestJson<AdminMountGrant>(`/api/v1/admin/mounts/${encodeURIComponent(mountId)}/grants/${encodeURIComponent(accountId)}`, {
    method: 'PUT',
    body: JSON.stringify({ permission }),
    signal,
  });
}

export function deleteAdminMountGrant(mountId: string, accountId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/admin/mounts/${encodeURIComponent(mountId)}/grants/${encodeURIComponent(accountId)}`, {
    method: 'DELETE',
    signal,
  });
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

export function listMemberContentSources(signal?: AbortSignal) {
  return requestJson<MemberContentSourcesPayload>('/api/v1/member/content-sources', { signal });
}

export function listMemberDirectoryChildren(locator: MemberContentLocator, signal?: AbortSignal) {
  const params = new URLSearchParams();
  params.set('source', locator.source);
  params.set('path', locator.path);
  if (locator.source === 'common_mount') params.set('mountId', locator.mountId);
  if (locator.source === 'collaboration') params.set('collaborationId', locator.collaborationId);
  return requestJson<DirectoryChildrenPayload>(`/api/v1/member/files/children?${params.toString()}`, { signal });
}

function memberLocatorBody(locator: MemberContentLocator) {
  return locator.source === 'personal'
    ? { source: locator.source, path: locator.path }
    : locator.source === 'common_mount'
      ? { source: locator.source, mountId: locator.mountId, path: locator.path }
      : { source: locator.source, collaborationId: locator.collaborationId, path: locator.path };
}

export function memberDownloadURL(locator: MemberContentLocator, options?: { inline?: boolean }) {
  const params = new URLSearchParams();
  params.set('source', locator.source);
  params.set('path', locator.path);
  if (locator.source === 'common_mount') params.set('mountId', locator.mountId);
  if (locator.source === 'collaboration') params.set('collaborationId', locator.collaborationId);
  if (options?.inline) params.set('inline', '1');
  return `/api/v1/member/files/download?${params.toString()}`;
}

export function searchMemberFiles(locator: MemberContentSourceLocator, query: string, limit = 50, cursor?: string, signal?: AbortSignal) {
  const params = new URLSearchParams();
  params.set('source', locator.source);
  if (locator.source === 'common_mount') params.set('mountId', locator.mountId);
  if (locator.source === 'collaboration') params.set('collaborationId', locator.collaborationId);
  params.set('q', query);
  params.set('limit', String(limit));
  if (cursor) params.set('cursor', cursor);
  return requestJson<SearchPayload>(`/api/v1/member/files/search?${params.toString()}`, { signal });
}

export function createMemberDirectory(locator: MemberContentLocator, name: string, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>('/api/v1/member/files/directories', {
    method: 'POST', body: JSON.stringify({ ...memberLocatorBody(locator), name }), signal,
  });
}

export function renameMemberObject(locator: MemberContentLocator, toPath: string, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>('/api/v1/member/files/rename', {
    method: 'POST', body: JSON.stringify({ ...memberLocatorBody(locator), toPath }), signal,
  });
}

export function moveMemberObject(source: MemberContentLocator, destination: MemberContentLocator, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>('/api/v1/member/files/move', {
    method: 'POST', body: JSON.stringify({ ...memberLocatorBody(source), toSource: destination.source, ...(destination.source === 'common_mount' ? { toMountId: destination.mountId } : {}), toPath: destination.path }), signal,
  });
}

export function copyMemberObject(source: MemberContentLocator, destination: MemberContentLocator, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>('/api/v1/member/files/copy', {
    method: 'POST', body: JSON.stringify({ ...memberLocatorBody(source), toSource: destination.source, ...(destination.source === 'common_mount' ? { toMountId: destination.mountId } : {}), toPath: destination.path }), signal,
  });
}

export function deleteMemberObject(locator: MemberContentLocator, options?: { permanent?: boolean }, signal?: AbortSignal) {
  const params = options?.permanent ? '?permanent=1' : '';
  return requestJson<TrashItemPayload | void>('/api/v1/member/files/object' + params, {
    method: 'DELETE', body: JSON.stringify(memberLocatorBody(locator)), signal,
  });
}

function memberLocatorQuery(locator: MemberContentLocator) {
  const params = new URLSearchParams();
  params.set('source', locator.source);
  params.set('path', locator.path);
  if (locator.source === 'common_mount') params.set('mountId', locator.mountId);
  if (locator.source === 'collaboration') params.set('collaborationId', locator.collaborationId);
  return params;
}

export function listMemberTrash(locator: MemberContentLocator, signal?: AbortSignal) {
  return requestJson<{ items: TrashItemPayload[] }>(`/api/v1/member/files/trash?${memberLocatorQuery(locator).toString()}`, { signal });
}

export function restoreMemberTrash(locator: MemberContentLocator, trashId: string, signal?: AbortSignal) {
  return requestJson<{ relativePath: string }>(`/api/v1/member/files/trash/${encodeURIComponent(trashId)}/restore`, {
    method: 'POST', body: JSON.stringify(memberLocatorBody(locator)), signal,
  });
}

export function purgeMemberTrash(locator: MemberContentLocator, trashId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/member/files/trash/${encodeURIComponent(trashId)}?${memberLocatorQuery(locator).toString()}`, { method: 'DELETE', signal });
}

export function emptyMemberTrash(locator: MemberContentLocator, signal?: AbortSignal) {
  return requestJson<{ removed: number }>(`/api/v1/member/files/trash?${memberLocatorQuery(locator).toString()}`, { method: 'DELETE', signal });
}
export function listMemberCollaborations(direction: 'incoming' | 'outgoing', signal?: AbortSignal) {
  const params = new URLSearchParams({ direction });
  return requestJson<MemberCollaborationsPayload>(`/api/v1/member/collaborations?${params.toString()}`, { signal });
}

export type CreateMemberCollaborationPayload = {
  recipientId: string;
  rootRelativePath: string;
  permission: 'viewer' | 'editor';
};

export function createMemberCollaboration(payload: CreateMemberCollaborationPayload, signal?: AbortSignal) {
  return requestJson<MemberCollaboration>('/api/v1/member/collaborations', { method: 'POST', body: JSON.stringify(payload), signal });
}

export function updateMemberCollaboration(collaborationId: string, permission: 'viewer' | 'editor', signal?: AbortSignal) {
  return requestJson<MemberCollaboration>(`/api/v1/member/collaborations/${encodeURIComponent(collaborationId)}`, { method: 'PATCH', body: JSON.stringify({ permission }), signal });
}

export function deleteMemberCollaboration(collaborationId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/member/collaborations/${encodeURIComponent(collaborationId)}`, { method: 'DELETE', signal });
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
  return requestJson<UploadSessionPayload>('/api/v1/member/uploads', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}

export function getUpload(uploadId: string, signal?: AbortSignal) {
  return requestJson<UploadSessionPayload>(`/api/v1/member/uploads/${encodeURIComponent(uploadId)}`, { signal });
}

export async function uploadPart(uploadId: string, partNumber: number, chunk: Blob | string, signal?: AbortSignal) {
  const headers = new Headers();
  const csrf = readCsrfCookie('account');
  if (csrf) {
    headers.set('X-CSRF-Token', csrf);
  }
  const response = await fetch(`/api/v1/member/uploads/${encodeURIComponent(uploadId)}/parts/${partNumber}`, {
    method: 'PUT',
    body: chunk,
    credentials: 'same-origin',
    headers,
    signal,
  });
  const body = await readJsonResponse(response);
  if (!response.ok) {
    throw new ApiError(`Request failed with ${response.status}`, response.status, body);
  }
}

function readCsrfCookie(authContext: APIAuthContext = 'account'): string | undefined {
  if (typeof document === 'undefined') {
    return undefined;
  }
  // Never infer share CSRF from path prefixes: /api/v1/shares must use the
  // account cookie pair, while only the share portal passes authContext:'share'.
  const names = authContext === 'share'
    ? ['__Host-omnora_share_csrf', 'omnora_dev_share_csrf']
    : ['__Host-omnora_csrf', 'omnora_dev_csrf'];
  for (const name of names) {
    const prefix = `${name}=`;
    const item = document.cookie.split(';').map((value) => value.trim()).find((value) => value.startsWith(prefix));
    if (item) {
      return decodeURIComponent(item.slice(prefix.length));
    }
  }
  return undefined;
}

export function completeUpload(uploadId: string, signal?: AbortSignal) {
  return requestJson<unknown>(`/api/v1/member/uploads/${encodeURIComponent(uploadId)}/complete`, {
    method: 'POST',
    signal,
  });
}

export function cancelUpload(uploadId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/member/uploads/${encodeURIComponent(uploadId)}`, {
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

export function getAdminRecovery(signal?: AbortSignal) {
  return requestJson<RecoveryControlPayload>('/api/v1/admin/recovery', { signal });
}

export function createAdminBackup(signal?: AbortSignal) {
  return requestJson<BackupPayload>('/api/v1/admin/backups', {
    method: 'POST',
    signal,
  });
}

export function restoreAdminBackup(backupId: string, confirmPhrase: string, signal?: AbortSignal) {
  return requestJson<{ status: string; requestId?: string; state?: string; backupId: string; notes?: string }>(`/api/v1/admin/backups/${encodeURIComponent(backupId)}/restore`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ confirmPhrase }),
    signal,
  });
}
