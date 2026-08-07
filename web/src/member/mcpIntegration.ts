import type { AiTokenScope } from '../api';

/** The transport contract exposed by the server and MCP Inspector. */
export const MCP_PATH = '/mcp' as const;
export const MCP_PROTOCOL_VERSION = '2026-07-28' as const;
export const MCP_TRANSPORT = 'Streamable HTTP' as const;
export const MCP_ERA = 'modern' as const;
export const MCP_OAUTH_STATUS = 'NOT IMPLEMENTED' as const;

/**
 * Keep this order in sync with the server's allowlist. It is also the order
 * used when displaying scopes in the token creation form.
 */
export const MCP_SCOPES = [
  'spaces:read',
  'files:list',
  'files:metadata',
  'files:text',
  'files:download_ticket',
  'search:read',
  'uploads:create',
  'files:write',
  'files:trash',
  'trash:read',
  'files:restore',
  'files:purge',
  'shares:read',
  'shares:create',
  'shares:revoke',
] as const satisfies readonly AiTokenScope[];

export const MCP_PRESETS = {
  readOnly: MCP_SCOPES.slice(0, 6),
  fileManagement: [
    ...MCP_SCOPES.slice(0, 6),
    'uploads:create', 'files:write', 'files:trash', 'trash:read', 'files:restore',
  ],
  shareManagement: [
    ...MCP_SCOPES.slice(0, 6),
    'shares:read', 'shares:create', 'shares:revoke',
  ],
  // Deliberately separate and opt-in. This scope is not implied by any
  // read, file-management, or share-management preset.
  permanentDelete: ['files:purge'],
} as const satisfies Record<string, readonly AiTokenScope[]>;

export type McpPreset = keyof typeof MCP_PRESETS;

export type McpToolSpec = {
  name: string;
  scope: AiTokenScope;
  /** Tools that require per-operation human confirmation (MRTR). */
  highRisk: boolean;
};

/**
 * Mirrors the runtime catalog in internal/mcpapi and docs/mcp/README.md.
 * Keep the 24 tools, their scopes, and the high-risk flags in sync with the
 * server; tools without the caller's scopes are omitted from tools/list.
 */
export const MCP_TOOL_CATALOG = [
  { name: 'spaces.list', scope: 'spaces:read', highRisk: false },
  { name: 'mounts.list', scope: 'spaces:read', highRisk: false },
  { name: 'files.list', scope: 'files:list', highRisk: false },
  { name: 'files.metadata', scope: 'files:metadata', highRisk: false },
  { name: 'files.search', scope: 'search:read', highRisk: false },
  { name: 'files.read_text', scope: 'files:text', highRisk: false },
  { name: 'files.prepare_download', scope: 'files:download_ticket', highRisk: false },
  { name: 'directories.create', scope: 'files:write', highRisk: false },
  { name: 'files.prepare_upload', scope: 'uploads:create', highRisk: false },
  { name: 'uploads.status', scope: 'uploads:create', highRisk: false },
  { name: 'uploads.complete', scope: 'uploads:create', highRisk: false },
  { name: 'uploads.cancel', scope: 'uploads:create', highRisk: false },
  { name: 'files.rename', scope: 'files:write', highRisk: false },
  { name: 'files.copy', scope: 'files:write', highRisk: false },
  { name: 'trash.list', scope: 'trash:read', highRisk: false },
  { name: 'trash.restore', scope: 'files:restore', highRisk: false },
  { name: 'shares.list', scope: 'shares:read', highRisk: false },
  { name: 'files.move', scope: 'files:write', highRisk: true },
  { name: 'files.trash', scope: 'files:trash', highRisk: true },
  { name: 'trash.purge', scope: 'files:purge', highRisk: true },
  { name: 'trash.empty', scope: 'files:purge', highRisk: true },
  { name: 'files.delete_permanently', scope: 'files:purge', highRisk: true },
  { name: 'shares.create', scope: 'shares:create', highRisk: true },
  { name: 'shares.revoke', scope: 'shares:revoke', highRisk: true },
] as const satisfies readonly McpToolSpec[];

export type InspectorConnection = {
  endpoint: string;
  transport: typeof MCP_TRANSPORT;
  protocolVersion: typeof MCP_PROTOCOL_VERSION;
  era: typeof MCP_ERA;
  authorization: string;
  oauth: typeof MCP_OAUTH_STATUS;
  text: string;
};

type RouteGroupLike = { id: string; exposed: boolean; entry?: string; risk?: string; tone?: string };

function currentOrigin() {
  return typeof globalThis.location?.origin === 'string' ? globalThis.location.origin : '';
}

export function getMcpEndpoint(origin = currentOrigin()) {
  const normalized = origin.trim().replace(/\/+$/, '');
  return `${normalized}${MCP_PATH}`;
}

export function buildInspectorConnection(origin: string, bearerToken: string): InspectorConnection {
  const endpoint = getMcpEndpoint(origin);
  const authorization = `Bearer ${bearerToken}`;
  const text = [
    `URL: ${endpoint}`,
    `Transport: ${MCP_TRANSPORT}`,
    `Protocol: ${MCP_PROTOCOL_VERSION}`,
    `Mode: ${MCP_ERA}`,
    `Authorization: ${authorization}`,
    `OAuth: ${MCP_OAUTH_STATUS}`,
  ].join('\n');
  return {
    endpoint,
    transport: MCP_TRANSPORT,
    protocolVersion: MCP_PROTOCOL_VERSION,
    era: MCP_ERA,
    authorization,
    oauth: MCP_OAUTH_STATUS,
    text,
  };
}

export function getMcpRouteState(
  bootstrap: { routeGroups?: readonly RouteGroupLike[] } | null | undefined,
  origin = currentOrigin(),
) {
  const group = bootstrap?.routeGroups?.find((candidate) => candidate.id === 'mcp');
  return { exposed: group?.exposed === true, endpoint: getMcpEndpoint(origin) };
}

// Readable aliases for consumers that prefer noun-oriented names.
export const mcpEndpoint = getMcpEndpoint;
export const inspectorConnectionText = (origin: string, bearerToken: string) => buildInspectorConnection(origin, bearerToken).text;
