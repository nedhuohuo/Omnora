import { describe, expect, it } from 'vitest';

import {
  MCP_ERA,
  MCP_OAUTH_STATUS,
  MCP_PATH,
  MCP_PROTOCOL_VERSION,
  MCP_SCOPES,
  MCP_TOOL_CATALOG,
  MCP_TRANSPORT,
  MCP_PRESETS,
  buildInspectorConnection,
  getMcpEndpoint,
  getMcpRouteState,
} from './mcpIntegration';

describe('MCP frontend contract', () => {
  it('pins the standard endpoint and protocol metadata', () => {
    expect(MCP_PATH).toBe('/mcp');
    expect(MCP_PROTOCOL_VERSION).toBe('2026-07-28');
    expect(MCP_TRANSPORT).toBe('Streamable HTTP');
    expect(MCP_ERA).toBe('modern');
    expect(MCP_OAUTH_STATUS).toBe('NOT IMPLEMENTED');
    expect(getMcpEndpoint('https://files.example.test/')).toBe('https://files.example.test/mcp');
    expect(getMcpEndpoint('https://files.example.test')).toBe('https://files.example.test/mcp');
  });

  it('keeps the fifteen scopes stable and maps the four presets', () => {
    expect(MCP_SCOPES).toEqual([
      'mounts:read',
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
    ]);
    expect(MCP_PRESETS.readOnly).toEqual(MCP_SCOPES.slice(0, 6));
    expect(MCP_PRESETS.fileManagement).toEqual([
      ...MCP_PRESETS.readOnly,
      'uploads:create', 'files:write', 'files:trash', 'trash:read', 'files:restore',
    ]);
    expect(MCP_PRESETS.shareManagement).toEqual([
      ...MCP_PRESETS.readOnly,
      'shares:read', 'shares:create', 'shares:revoke',
    ]);
    expect(MCP_PRESETS.permanentDelete).toEqual(['files:purge']);
  });

  it('exposes the account-mount catalog without the legacy spaces.list tool', () => {
    expect(MCP_TOOL_CATALOG).toHaveLength(24);
    expect(new Set(MCP_TOOL_CATALOG.map((tool) => tool.name)).size).toBe(24);
    expect(MCP_TOOL_CATALOG.map((tool) => tool.name)).not.toContain('spaces.list');
    expect(MCP_TOOL_CATALOG).toContainEqual({ name: 'mounts.list', scope: 'mounts:read', highRisk: false });
    const highRisk = MCP_TOOL_CATALOG.filter((tool) => tool.highRisk).map((tool) => tool.name);
    expect(highRisk).toEqual([
      'files.move',
      'files.trash',
      'trash.purge',
      'trash.empty',
      'files.delete_permanently',
      'shares.create',
      'shares.revoke',
      'files.update',
    ]);
    expect(new Set(MCP_TOOL_CATALOG.map((tool) => tool.scope))).toEqual(new Set(MCP_SCOPES));
  });

  it('builds a same-origin Inspector connection block without OAuth claims', () => {
    const connection = buildInspectorConnection('https://files.example.test', 'public.secret');
    expect(connection).toEqual({
      endpoint: 'https://files.example.test/mcp',
      transport: 'Streamable HTTP',
      protocolVersion: '2026-07-28',
      era: 'modern',
      authorization: 'Bearer public.secret',
      oauth: 'NOT IMPLEMENTED',
      text: expect.stringContaining('Authorization: Bearer public.secret'),
    });
  });

  it('reports route state from bootstrap without treating entry as a URL', () => {
    expect(getMcpRouteState({ routeGroups: [{ id: 'mcp', exposed: true, entry: 'http', risk: '', tone: '' }] }, 'https://files.example.test')).toEqual({
      exposed: true,
      endpoint: 'https://files.example.test/mcp',
    });
    expect(getMcpRouteState({ routeGroups: [] }, 'https://files.example.test')).toEqual({
      exposed: false,
      endpoint: 'https://files.example.test/mcp',
    });
  });
});
