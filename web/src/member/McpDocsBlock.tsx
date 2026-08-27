import { useState } from 'react';
import type { AiTokenScope } from '../api';
import { copyText } from './clipboard';
import { type MemberLocale, localeMessages } from './i18n';
import {
  MCP_OAUTH_STATUS,
  MCP_PROTOCOL_VERSION,
  MCP_SCOPES,
  MCP_TOOL_CATALOG,
  MCP_TRANSPORT,
} from './mcpIntegration';

/**
 * Shared MCP status card + documentation block. The Docs panel shows the full
 * documentation (showDocs), the AI Token panel shows only the status card.
 * Keeps endpoint, protocol, scope and tool catalog documentation in one place
 * so both views stay in sync with mcpIntegration.
 */
export default function McpDocsBlock({ endpoint, exposed, locale, showDocs = true }: {
  endpoint: string;
  exposed: boolean;
  locale: MemberLocale;
  showDocs?: boolean;
}) {
  const text = localeMessages[locale];
  const [copied, setCopied] = useState(false);
  const [endpointCopied, setEndpointCopied] = useState(false);
  const configExample = JSON.stringify({
    mcpServers: {
      omnora: {
        url: endpoint,
        headers: { Authorization: 'Bearer <YOUR_TOKEN>' },
      },
    },
  }, null, 2);

  async function onCopyConfig() {
    if (await copyText(configExample)) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    }
  }

  async function onCopyEndpoint() {
    if (await copyText(endpoint)) {
      setEndpointCopied(true);
      window.setTimeout(() => setEndpointCopied(false), 2000);
    }
  }

  const scopeDescriptions: Record<AiTokenScope, string> = {
    'mounts:read': text.tokenMcpScopeMountsRead,
    'files:list': text.tokenMcpScopeFilesList,
    'files:metadata': text.tokenMcpScopeFilesMetadata,
    'files:text': text.tokenMcpScopeFilesText,
    'files:download_ticket': text.tokenMcpScopeFilesDownloadTicket,
    'search:read': text.tokenMcpScopeSearchRead,
    'uploads:create': text.tokenMcpScopeUploadsCreate,
    'files:write': text.tokenMcpScopeFilesWrite,
    'files:trash': text.tokenMcpScopeFilesTrash,
    'trash:read': text.tokenMcpScopeTrashRead,
    'files:restore': text.tokenMcpScopeFilesRestore,
    'files:purge': text.tokenMcpScopeFilesPurge,
    'shares:read': text.tokenMcpScopeSharesRead,
    'shares:create': text.tokenMcpScopeSharesCreate,
    'shares:revoke': text.tokenMcpScopeSharesRevoke,
  };
  const docs = (
    <div className="member-mcp-docs-body">
      <h3>{text.tokenMcpDocsClientTitle}</h3>
      <p>{text.tokenMcpDocsClientHint}</p>
      <h3>{text.docsMcpConfigTitle}</h3>
      <p>{text.docsMcpConfigHint}</p>
      <div className="member-code-block">
        <pre>{configExample}</pre>
        <button className="member-code-copy" type="button" onClick={() => void onCopyConfig()}>{copied ? text.copied : text.copy}</button>
      </div>
      <ol>
        <li>{text.tokenMcpDocsStep1}</li>
        <li>{text.tokenMcpDocsStep2}</li>
        <li>{text.tokenMcpDocsStep3}</li>
      </ol>
      <h3>{text.tokenMcpDocsInspectorTitle}</h3>
      <pre>{`URL: ${endpoint}
Transport: ${MCP_TRANSPORT}
Protocol: ${MCP_PROTOCOL_VERSION}
Authorization: Bearer <AI_TOKEN>
OAuth: ${MCP_OAUTH_STATUS}`}</pre>
      <p>{text.tokenMcpDocsInspectorHint}</p>
      <h3>{text.tokenMcpDocsScopeTitle}</h3>
      <p>{text.tokenMcpDocsScopeHint}</p>
      <table className="member-mcp-docs-table">
        <thead><tr><th>{text.tokenMcpDocsScopeColumn}</th><th>{text.tokenMcpDocsScopeDescColumn}</th></tr></thead>
        <tbody>{MCP_SCOPES.map((scope) => (
          <tr key={scope}><td><code>{scope}</code></td><td>{scopeDescriptions[scope]}</td></tr>
        ))}</tbody>
      </table>
      <h3>{text.tokenMcpDocsToolsTitle}</h3>
      <table className="member-mcp-docs-table">
        <thead><tr><th>{text.tokenMcpDocsToolColumn}</th><th>{text.tokenMcpDocsScopeColumn}</th><th>{text.tokenMcpDocsRiskColumn}</th></tr></thead>
        <tbody>{MCP_TOOL_CATALOG.map((tool) => (
          <tr key={tool.name}>
            <td><code>{tool.name}</code></td>
            <td><code>{tool.scope}</code></td>
            <td className={tool.highRisk ? 'member-mcp-docs-risk' : 'member-mcp-docs-risk-no'}>{tool.highRisk ? text.tokenMcpDocsRiskYes : text.tokenMcpDocsRiskNo}</td>
          </tr>
        ))}</tbody>
      </table>
      <h3>{text.tokenMcpDocsHighRiskTitle}</h3>
      <p>{text.tokenMcpDocsHighRiskDetail}</p>
      <h3>{text.tokenMcpDocsTransfersTitle}</h3>
      <p>{text.tokenMcpDocsTransfersDetail}</p>
      <h3>{text.tokenMcpDocsLifecycleTitle}</h3>
      <ul>
        <li>{text.tokenMcpDocsLifecyclePlaintext}</li>
        <li>{text.tokenMcpDocsLifecycleRevoke}</li>
        <li>{text.tokenMcpDocsLifecycleExpiry}</li>
        <li>{text.tokenMcpDocsLifecycleBoundary}</li>
        <li>{text.tokenMcpDocsLifecycleSecret}</li>
      </ul>
    </div>
  );

  return (
    <section className="member-mcp-status" aria-label={text.tokenMcpStatusTitle}>
      <div className="member-mcp-status-heading">
        <div><h2>{text.tokenMcpStatusTitle}</h2><p>{text.tokenMcpStatusDetail}</p></div>
        <div className="member-mcp-status-heading-meta">
          <span className={`member-route-badge ${exposed ? 'exposed' : 'closed'}`}>{exposed ? text.routeExposed : text.routeClosed}</span>
          <span className="member-mcp-status-heading-hint">{exposed ? text.routeDisableNextRequest.replace('关闭后', '启用中；关闭后') : text.routeDisableNextRequest}</span>
        </div>
      </div>
      <div className="member-mcp-status-grid">
        <div>
          <span>{text.tokenMcpEndpoint}</span>
          <div className="member-mcp-endpoint-row">
            <code className="member-mcp-endpoint-code" title={endpoint}>{endpoint}</code>
            <button className="member-mcp-endpoint-copy" type="button" onClick={() => void onCopyEndpoint()}>{endpointCopied ? text.copied : text.copy}</button>
          </div>
        </div>
        <div><span>{text.tokenMcpProtocol}</span><strong>{MCP_PROTOCOL_VERSION}</strong></div>
        <div><span>{text.tokenMcpTransport}</span><strong>{MCP_TRANSPORT}</strong></div>
        <div><span>{text.tokenMcpAuth}</span><code>Authorization: Bearer &lt;AI_TOKEN&gt;</code></div>
      </div>
      <div className="member-mcp-oauth-row">
        <span>{text.tokenMcpOAuth}</span>
        <span className="member-mcp-oauth-pill">{MCP_OAUTH_STATUS}</span>
        <span className="member-mcp-oauth-desc">{locale === 'zh-CN' ? '当前仅支持 Bearer Token，预留 OAuth 扩展位' : 'Bearer Token only; OAuth reserved'}</span>
      </div>
      {showDocs && <div className="member-mcp-docs">{docs}</div>}
    </section>
  );
}
