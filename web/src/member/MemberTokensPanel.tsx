import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AiTokenBoundary,
  type AiTokenListItem,
  ApiError,
  createAiToken,
  listAiTokens,
  listMounts,
  listSpaces,
  revokeAiToken,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import type { MemberMount, MemberSpace } from './types';
import { createClientId } from './clientId';
import { copyText } from './clipboard';

// Canonical backend scopes (see internal/aitoken/types.go). The UI presents a
// simplified "read" / "upload" choice; read expands to the full read-only set.
const READ_SCOPES = ['spaces:read', 'files:list', 'files:metadata', 'files:text', 'search:read'];

type LocaleText = (typeof localeMessages)[MemberLocale];

function describeError(error: unknown) {
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

function boundarySummary(boundary: AiTokenBoundary) {
  const path = boundary.path && boundary.path !== '.' ? boundary.path : '/';
  return `${boundary.spaceId} / ${boundary.mountId} · ${path}`;
}

export default function MemberTokensPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [tokens, setTokens] = useState<AiTokenListItem[]>([]);
  const [spaces, setSpaces] = useState<MemberSpace[]>([]);
  const [mountsBySpace, setMountsBySpace] = useState<Record<string, MemberMount[]>>({});
  const [name, setName] = useState('');
  const [scopeRead, setScopeRead] = useState(true);
  const [scopeUpload, setScopeUpload] = useState(false);
  const [expiresAt, setExpiresAt] = useState('');
  const [boundaries, setBoundaries] = useState<BoundaryDraft[]>([]);
  const [formOpen, setFormOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [createdToken, setCreatedToken] = useState('');
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<AiTokenListItem | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [tokenResponse, spaceResponse] = await Promise.all([listAiTokens(), listSpaces()]);
      setTokens(tokenResponse.items ?? []);
      setSpaces(spaceResponse.items);
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
    setScopeRead(true);
    setScopeUpload(false);
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
      const scopes = [
        ...(scopeRead ? READ_SCOPES : []),
        ...(scopeUpload ? ['uploads:create'] : []),
      ];
      const result = await createAiToken({
        name: name.trim(),
        scopes: scopes.length > 0 ? scopes : READ_SCOPES,
        boundaries: validBoundaries.map((boundary) => ({
          spaceId: boundary.spaceId,
          mountId: boundary.mountId,
          path: boundary.path.trim() || '.',
        })),
        expiresAt: new Date(expiresAt).toISOString(),
      });
      setCreatedToken(result.bearerToken ?? result.secret ?? '');
      setFormOpen(false);
      resetForm();
      await load();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  async function onRevoke() {
    if (!revokeTarget) return;
    setLoading(true);
    setError('');
    try {
      await revokeAiToken(revokeTarget.id);
      setRevokeTarget(null);
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

      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

      {loading ? <div className="member-loading">{text.loading}</div> : tokens.length === 0 ? <div className="member-empty">{text.tokenListEmpty}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.tokenColumnName}</th><th>{text.tokenColumnScopes}</th><th>{text.tokenColumnBoundary}</th><th>{text.tokenColumnExpires}</th><th>{text.tokenColumnStatus}</th><th>{text.actions}</th></tr></thead>
          <tbody>{tokens.map((token) => (
            <tr key={token.id}>
              <td>{token.name}<small>{token.publicId ?? token.id}</small></td>
              <td>{(token.scopes ?? []).join(', ') || '--'}</td>
              <td>{(token.boundaries ?? []).length === 0 ? '--' : token.boundaries!.map((boundary) => boundarySummary(boundary)).join('; ')}</td>
              <td>{formatDate(token.expiresAt, locale, '--')}</td>
              <td>{tokenStatusLabel(token.status, text)}</td>
              <td><button className="member-table-action member-table-danger" type="button" onClick={() => setRevokeTarget(token)} disabled={loading}>{text.tokenRevoke}</button></td>
            </tr>
          ))}</tbody>
        </table>
      )}

      {formOpen && (
        <div className="member-modal-backdrop">
          <form className="member-modal member-admin-form" onSubmit={onSubmit}>
            <h2 className="member-admin-form-wide">{text.tokenCreate}</h2>
            <label className="member-admin-form-wide">{text.tokenName}<input value={name} onChange={(event) => setName(event.target.value)} required /></label>
            <label className="member-admin-checkbox"><input type="checkbox" checked={scopeRead} onChange={(event) => setScopeRead(event.target.checked)} />{text.tokenScopeRead}</label>
            <label className="member-admin-checkbox"><input type="checkbox" checked={scopeUpload} onChange={(event) => setScopeUpload(event.target.checked)} />{text.tokenScopeUpload}</label>
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
            <code className="member-share-url">{createdToken}</code>
            <div className="member-share-result-actions">
              <button type="button" onClick={() => void copyText(createdToken).then((ok) => { setCopied(ok); setCopyFailed(!ok); })}>{text.tokenCopy}</button>
            </div>
            {copied && <p className="member-admin-notice">{text.tokenCopied}</p>}
            {copyFailed && <p className="member-error">{text.tokenCopyFailed}</p>}
            <div><button className="member-primary" type="button" onClick={() => { setCreatedToken(''); setCopied(false); setCopyFailed(false); }}>{text.tokenClose}</button></div>
          </div>
        </div>
      )}

      {revokeTarget && (
        <div className="member-modal-backdrop">
          <div className="member-modal">
            <h2>{text.tokenRevokeConfirmTitle}</h2>
            <p className="member-modal-hint">{text.tokenRevokeConfirmDetail}</p>
            <p className="member-modal-hint"><strong>{revokeTarget.name}</strong></p>
            <div><button type="button" onClick={() => setRevokeTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="button" onClick={() => void onRevoke()} disabled={loading}>{text.tokenRevoke}</button></div>
          </div>
        </div>
      )}
    </div>
  );
}
