import type { AppBootstrap } from './types';

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
  entries?: unknown[];
  items?: unknown[];
  nextCursor?: string;
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
  items?: unknown[];
  excludedMounts?: unknown[];
  nextCursor?: string;
};

export type JobPayload = {
  id?: string;
  status?: string;
  kind?: string;
  payload?: unknown;
};

export type AuditEventsPayload = {
  items?: unknown[];
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

export function searchSpace(spaceId: string, query: string, limit = 20, signal?: AbortSignal) {
  const params = new URLSearchParams({ q: query, limit: String(limit) });
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
  return requestJson<{ items?: unknown[] }>('/api/v1/ai-tokens', { signal });
}

export function revokeAiToken(tokenId: string, signal?: AbortSignal) {
  return requestJson<void>(`/api/v1/ai-tokens/${encodeURIComponent(tokenId)}`, {
    method: 'DELETE',
    signal,
  });
}

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
  return requestJson<{ items?: unknown[] }>('/api/v1/admin/index-jobs', { signal });
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
