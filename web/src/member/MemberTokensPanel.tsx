import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AiTokenScope,
  type AiTokenBoundary,
  type AiTokenListItem,
  ApiError,
  createAiToken,
  deleteAiToken,
  getBootstrap,
  isReauthenticationCanceled,
  listAiTokens,
  listMounts,
  listSpaces,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { useRecentReauth } from './RecentReauthProvider';
import type { MemberMount, MemberSpace } from './types';
import { createClientId } from './clientId';
import { copyText } from './clipboard';
import { joinReadableLabels, readableLabel } from './displayLabels';
import {
  MCP_OAUTH_STATUS,
  MCP_PRESETS,
  MCP_PROTOCOL_VERSION,
  MCP_TRANSPORT,
  buildInspectorConnection,
  getMcpRouteState,
  type InspectorConnection,
  type McpPreset,
} from './mcpIntegration';

type LocaleText = (typeof localeMessages)[MemberLocale];

function describeError(error: unknown) {
  if (isReauthenticationCanceled(error)) return '';
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    const message = body?.error?.message?.trim();
    if (message) return `HTTP ${error.status}: ${message}`;
    if (body?.error?.code) return `HTTP ${error.status}: ${body.error.code}`;
    return `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : 'Unknown error';
}

function formatDate(value: string | undefined, locale: MemberLocale, fallback: string) {
  if (!value) return fallback;
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

function tokenStatusLabel(status: string | undefined, text: LocaleText) {
  const labels: Record<string, string> = {
    active: text.tokenStatusActive,
    expired: text.tokenStatusExpired,
    revoked: text.tokenStatusRevoked,
  };
  return status ? (labels[status] ?? status) : text.tokenStatusActive;
}

type BoundaryDraft = { key: string; spaceId: string; mountId: string; path: string };

function boundarySummary(boundary: AiTokenBoundary, spaces: MemberSpace[], mountsBySpace: Record<string, MemberMount[]>) {
  const path = boundary.path && boundary.path !== '.' ? boundary.path : '/';
  const spaceName = readableLabel(boundary.spaceName) || spaces.find((space) => space.id === boundary.spaceId)?.name;
  const mountName = readableLabel(boundary.mountName) || mountsBySpace[boundary.spaceId]?.find((mount) => mount.id === boundary.mountId)?.name;
  const location = joinReadableLabels([spaceName, mountName]);
  return location ? `${location} · ${path}` : path;
}

export default function MemberTokensPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const { runSensitive } = useRecentReauth();
  const [tokens, setTokens] = useState<AiTokenListItem[]>([]);
  const [spaces, setSpaces] = useState<MemberSpace[]>([]);
  const [mountsBySpace, setMountsBySpace] = useState<Record<string, MemberMount[]>>({});
  const [name, setName] = useState('');
  const [preset, setPreset] = useState<Exclude<McpPreset, 'permanentDelete'>>('readOnly');
  const [permanentDelete, setPermanentDelete] = useState(false);
  const [expiresAt, setExpiresAt] = useState('');
  const [boundaries, setBoundaries] = useState<BoundaryDraft[]>([]);
  const [formOpen, setFormOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [createdToken, setCreatedToken] = useState<{ bearerToken: string; connection: InspectorConnection } | null>(null);
  const [copied, setCopied] = useState(false);
  const [connectionCopied, setConnectionCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AiTokenListItem | null>(null);
  const [bootstrap, setBootstrap] = useState<{ routeGroups?: Array<{ id: string; exposed: boolean }> } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [tokenResponse, spaceResponse, bootstrapResponse] = await Promise.all([listAiTokens(), listSpaces(), getBootstrap()]);
      const nextTokens = tokenResponse.items ?? [];
      setTokens(nextTokens);
      setSpaces(spaceResponse.items);
      setBootstrap(bootstrapResponse);
      const tokenSpaceIds = Array.from(new Set(nextTokens.flatMap((token) => (token.boundaries ?? []).map((boundary) => boundary.spaceId)).filter(Boolean)));
      if (tokenSpaceIds.length > 0) {
        const mountEntries = await Promise.all(tokenSpaceIds.map(async (spaceId) => {
          try {
            const response = await listMounts(spaceId);
            return [spaceId, response.items] as const;
          } catch {
            return [spaceId, []] as const;
          }
        }));
        setMountsBySpace((current) => ({ ...current, ...Object.fromEntries(mountEntries) }));
      }
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function addBoundary() {
    const firstSpace = spaces[0]?.id ?? '';
    setBoundaries((current) => [...current, { key: createClientId(), spaceId: firstSpace, mountId: '', path: '' }]);
  }

  function removeBoundary(key: string) {
    setBoundaries((current) => current.filter((boundary) => boundary.key !== key));
  }

  function updateBoundary(key: string, patch: Partial<BoundaryDraft>) {
    setBoundaries((current) => current.map((boundary) => (boundary.key === key ? { ...boundary, ...patch } : boundary)));
  }

  useEffect(() => {
    boundaries.forEach((boundary) => {
      if (boundary.spaceId && !mountsBySpace[boundary.spaceId]) {
        void listMounts(boundary.spaceId).then((response) => {
          setMountsBySpace((current) => ({ ...current, [boundary.spaceId]: response.items }));
        }).catch(() => undefined);
      }
    });
  }, [boundaries, mountsBySpace]);

  function resetForm() {
    setName('');
    setPreset('readOnly');
    setPermanentDelete(false);
    setExpiresAt('');
    setBoundaries([]);
  }

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!name.trim() || !expiresAt) return;
    setCreating(true);
    setError('');
    try {
      const validBoundaries = boundaries.filter((boundary) => boundary.spaceId && boundary.mountId);
      if (validBoundaries.length === 0) {
        setError(text.tokenBoundaryRequired);
        return;
      }
      const scopes: AiTokenScope[] = [
        ...MCP_PRESETS[preset],
        ...(permanentDelete ? MCP_PRESETS.permanentDelete : []),
      ];
      const result = await runSensitive(() => createAiToken({
        name: name.trim(),
        scopes,
        boundaries: validBoundaries.map((boundary) => ({
          spaceId: boundary.spaceId,
          mountId: boundary.mountId,
          path: boundary.path.trim() || '.',
        })),
        expiresAt: new Date(expiresAt).toISOString(),
      }));
      const connection = buildInspectorConnection(globalThis.location?.origin ?? '', result.bearerToken);
      setCreatedToken({ bearerToken: result.bearerToken, connection });
      setCopied(false);
      setConnectionCopied(false);
      setFormOpen(false);
      resetForm();
      await load();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  const routeState = getMcpRouteState(bootstrap);
  const presetOptions: Array<{ id: Exclude<McpPreset, 'permanentDelete'>; label: string; detail: string }> = [
    { id: 'readOnly', label: text.tokenPresetReadOnly, detail: text.tokenPresetReadOnlyDetail },
    { id: 'fileManagement', label: text.tokenPresetFileManagement, detail: text.tokenPresetFileManagementDetail },
    { id: 'shareManagement', label: text.tokenPresetShareManagement, detail: text.tokenPresetShareManagementDetail },
  ];

  function closeCreatedToken() {
    setCreatedToken(null);
    setCopied(false);
    setConnectionCopied(false);
    setCopyFailed(false);
  }

  async function onDelete() {
    if (!deleteTarget) return;
    setLoading(true);
    setError('');
    try {
      await runSensitive(() => deleteAiToken(deleteTarget.id));
      setDeleteTarget(null);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.tokensTitle}</h1><p>{text.tokensTitleDetail}</p></div>
        <div className="member-admin-table-actions">
          <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
          <button className="member-primary" type="button" onClick={() => setFormOpen(true)}>{text.tokenCreate}</button>
        </div>
      </div>

      <section className="member-mcp-status" aria-label={text.tokenMcpStatusTitle}>
        <div className="member-mcp-status-heading">
          <div><h2>{text.tokenMcpStatusTitle}</h2><p>{text.tokenMcpStatusDetail}</p></div>
          <span className={`member-route-badge ${routeState.exposed ? 'exposed' : 'closed'}`}>{routeState.exposed ? text.routeExposed : text.routeClosed}</span>
        </div>
        <div className="member-mcp-status-grid">
          <div><span>{text.tokenMcpEndpoint}</span><code>{routeState.endpoint}</code></div>
          <div><span>{text.tokenMcpProtocol}</span><strong>{MCP_PROTOCOL_VERSION}</strong></div>
          <div><span>{text.tokenMcpTransport}</span><strong>{MCP_TRANSPORT}</strong></div>
          <div><span>{text.tokenMcpAuth}</span><code>Authorization: Bearer &lt;AI_TOKEN&gt;</code></div>
          <div><span>{text.tokenMcpOAuth}</span><strong>{MCP_OAUTH_STATUS}</strong></div>
        </div>
      </section>

      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

      {loading ? <div className="member-loading">{text.loading}</div> : tokens.length === 0 ? <div className="member-empty">{text.tokenListEmpty}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.tokenColumnName}</th><th>{text.tokenColumnScopes}</th><th>{text.tokenColumnBoundary}</th><th>{text.tokenColumnExpires}</th><th>{text.tokenColumnStatus}</th><th>{text.actions}</th></tr></thead>
          <tbody>{tokens.map((token) => (
            <tr key={token.id}>
              <td>{token.name}</td>
              <td>{(token.scopes ?? []).join(', ') || '--'}</td>
              <td>{(token.boundaries ?? []).length === 0 ? '--' : token.boundaries!.map((boundary) => boundarySummary(boundary, spaces, mountsBySpace)).join('; ')}</td>
              <td>{formatDate(token.expiresAt, locale, '--')}</td>
              <td>{tokenStatusLabel(token.status, text)}</td>
              <td><button className="member-table-action member-table-danger" type="button" onClick={() => setDeleteTarget(token)} disabled={loading}>{text.tokenRevoke}</button></td>
            </tr>
          ))}</tbody>
        </table>
      )}

      {formOpen && (
        <div className="member-modal-backdrop">
          <form className="member-modal member-admin-form" onSubmit={onSubmit}>
            <h2 className="member-admin-form-wide">{text.tokenCreate}</h2>
            <label className="member-admin-form-wide">{text.tokenName}<input value={name} onChange={(event) => setName(event.target.value)} required /></label>
            <fieldset className="member-admin-form-wide member-mcp-presets">
              <legend>{text.tokenPresetTitle}</legend>
              <div className="member-mcp-preset-grid">
                {presetOptions.map((option) => (
                  <label className={`member-mcp-preset ${preset === option.id ? 'selected' : ''}`} key={option.id}>
                    <input type="radio" name="mcp-preset" value={option.id} checked={preset === option.id} onChange={() => setPreset(option.id)} />
                    <span><strong>{option.label}</strong><small>{option.detail}</small></span>
                  </label>
                ))}
                <label className={`member-mcp-preset member-mcp-preset-danger ${permanentDelete ? 'selected' : ''}`}>
                  <input type="checkbox" checked={permanentDelete} onChange={(event) => setPermanentDelete(event.target.checked)} />
                  <span><strong>{text.tokenPresetPermanentDelete}</strong><small>{text.tokenPresetPermanentDeleteDetail}</small></span>
                </label>
              </div>
              <p className="member-mcp-warning">{text.tokenMcpHighRiskWarning}</p>
            </fieldset>
            <label className="member-admin-form-wide">{text.tokenExpiresAt}<input type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} required /></label>

            <div className="member-admin-form-wide member-token-boundaries">
              {boundaries.map((boundary) => (
                <div className="member-token-boundary-row" key={boundary.key}>
                  <select value={boundary.spaceId} onChange={(event) => updateBoundary(boundary.key, { spaceId: event.target.value, mountId: '' })}>
                    <option value="" disabled>{text.tokenBoundarySpace}</option>
                    {spaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}
                  </select>
                  <select value={boundary.mountId} onChange={(event) => updateBoundary(boundary.key, { mountId: event.target.value })}>
                    <option value="" disabled>{text.tokenBoundaryMount}</option>
                    {(mountsBySpace[boundary.spaceId] ?? []).map((mount) => <option key={mount.id} value={mount.id}>{mount.name}</option>)}
                  </select>
                  <input value={boundary.path} onChange={(event) => updateBoundary(boundary.key, { path: event.target.value })} placeholder={text.tokenBoundaryPath} />
                  <button type="button" onClick={() => removeBoundary(boundary.key)} aria-label={text.tokenBoundaryRemove} title={text.tokenBoundaryRemove}>×</button>
                </div>
              ))}
              <button type="button" onClick={addBoundary}>{text.tokenAddBoundary}</button>
            </div>

            {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
            <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={() => setFormOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={creating}>{text.tokenSubmit}</button></div>
          </form>
        </div>
      )}

      {createdToken && (
        <div className="member-modal-backdrop">
          <div className="member-modal member-share-result">
            <h2>{text.tokenCreatedTitle}</h2>
            <p className="member-modal-hint">{text.tokenCreatedHint}</p>
            <code className="member-share-url">{createdToken.bearerToken}</code>
            <div className="member-share-result-actions">
              <button type="button" onClick={() => void copyText(createdToken.bearerToken).then((ok) => { setCopied(ok); setCopyFailed(!ok); })}>{text.tokenCopy}</button>
            </div>
            {copied && <p className="member-admin-notice">{text.tokenCopied}</p>}
            {copyFailed && <p className="member-error">{text.tokenCopyFailed}</p>}
            <div className="member-mcp-connection">
              <h3>{text.tokenMcpConnectionTitle}</h3>
              <dl className="member-mcp-connection-meta">
                <div><dt>{text.tokenMcpEndpoint}</dt><dd>{createdToken.connection.endpoint}</dd></div>
                <div><dt>{text.tokenMcpTransport}</dt><dd>{createdToken.connection.transport}</dd></div>
                <div><dt>{text.tokenMcpMode}</dt><dd>{createdToken.connection.era}</dd></div>
                <div><dt>{text.tokenMcpOAuth}</dt><dd>{createdToken.connection.oauth}</dd></div>
              </dl>
              <pre>{createdToken.connection.text}</pre>
              <button type="button" onClick={() => void copyText(createdToken.connection.text).then((ok) => { setConnectionCopied(ok); setCopyFailed(!ok); })}>{text.tokenMcpCopyConnection}</button>
              {connectionCopied && <p className="member-admin-notice">{text.tokenCopied}</p>}
            </div>
            <div><button className="member-primary" type="button" onClick={closeCreatedToken}>{text.tokenClose}</button></div>
          </div>
        </div>
      )}

      {deleteTarget && (
        <div className="member-modal-backdrop">
          <div className="member-modal">
            <h2>{text.tokenRevokeConfirmTitle}</h2>
            <p className="member-modal-hint">{text.tokenRevokeConfirmDetail}</p>
            <p className="member-modal-hint"><strong>{deleteTarget.name}</strong></p>
            <div><button type="button" onClick={() => setDeleteTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="button" onClick={() => void onDelete()} disabled={loading}>{text.tokenRevoke}</button></div>
          </div>
        </div>
      )}
    </div>
  );
}
