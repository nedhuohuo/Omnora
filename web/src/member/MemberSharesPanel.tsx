import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  ApiError,
  buildShareURL,
  createMemberCollaboration,
  createShare,
  deleteMemberCollaboration,
  deleteShare,
  listMemberCollaborations,
  listShares,
  type CreateShareResponse,
  type MemberCollaboration,
  type MemberContentLocator,
  type SharePayload,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { copyText } from './clipboard';

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

function formatDate(value: string | undefined, locale: MemberLocale, text: LocaleText) {
  if (!value) return text.never;
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

function shareStatusLabel(status: string | undefined, text: LocaleText) {
  const labels: Record<string, string> = {
    active: text.shareStatusActive,
    expired: text.shareStatusExpired,
    revoked: text.shareStatusRevoked,
    exhausted: text.shareStatusExhausted,
  };
  return status ? (labels[status] ?? status) : text.shareStatusActive;
}

function normalizeSharePath(path: string) {
  const trimmed = path.trim();
  if (!trimmed || trimmed === '.' || trimmed === '/') return '.';
  return trimmed.replace(/^\/+|\/+$/g, '');
}

function displaySharePath(path: string, text: LocaleText) {
  const normalized = normalizeSharePath(path);
  return normalized === '.' ? text.shareBrowseRoot : normalized;
}

function shareLocationLabel(share: SharePayload, text: LocaleText) {
  return share.source === 'personal' ? text.myFiles : share.mountName ?? text.commonStorage;
}

export type ShareOptionsState = {
  password: string;
  expiresAt: string;
  allowPreview: boolean;
  allowDownload: boolean;
};

export const defaultShareOptions: ShareOptionsState = {
  password: '',
  expiresAt: '',
  allowPreview: true,
  allowDownload: true,
};

export function ShareOptionFields({ text, value, onChange }: { text: LocaleText; value: ShareOptionsState; onChange: (next: ShareOptionsState) => void }) {
  return <>
    <label>{text.sharePasswordOptional}<input type="password" value={value.password} onChange={(event) => onChange({ ...value, password: event.target.value })} autoComplete="new-password" /></label>
    <label>{text.shareExpiresAt}<input type="datetime-local" value={value.expiresAt} onChange={(event) => onChange({ ...value, expiresAt: event.target.value })} /><small className="member-path-hint">{text.shareNeverExpires}</small></label>
    <label className="member-admin-checkbox"><input type="checkbox" checked={value.allowPreview} onChange={(event) => onChange({ ...value, allowPreview: event.target.checked })} />{text.shareAllowPreview}</label>
    <label className="member-admin-checkbox"><input type="checkbox" checked={value.allowDownload} onChange={(event) => onChange({ ...value, allowDownload: event.target.checked })} />{text.shareAllowDownload}</label>
  </>;
}

export function buildCreateSharePayload(locator: MemberContentLocator, options: ShareOptionsState) {
  const expiresAt = options.expiresAt ? new Date(options.expiresAt).toISOString() : undefined;
  const base = locator.source === 'personal'
    ? { source: 'personal' as const }
    : locator.source === 'common_mount'
      ? { source: 'common_mount' as const, mountId: locator.mountId }
      : { source: 'collaboration' as const, collaborationId: locator.collaborationId };
  return {
    ...base,
    relativePath: normalizeSharePath(locator.path),
    password: options.password.trim() || undefined,
    allowPreview: options.allowPreview,
    allowDownload: options.allowDownload,
    expiresAt,
  };
}

export function resolveShareFragment(result: CreateShareResponse) {
  if (result.fragment) return result.fragment;
  if (result.publicId && result.secret) return `${result.publicId}.${result.secret}`;
  return '';
}

export function ShareCreatedResult({ text, result, onClose }: { text: LocaleText; result: CreateShareResponse; onClose: () => void }) {
  const fragment = resolveShareFragment(result);
  const url = fragment ? buildShareURL(fragment) : '';
  const [copied, setCopied] = useState<'url' | 'secret' | 'failed' | null>(null);
  return <div className="member-modal-backdrop"><div className="member-modal member-share-result">
    <h2>{text.shareCreatedTitle}</h2><p className="member-modal-hint">{text.shareCreatedHint}</p><code className="member-share-url">{url}</code>
    <div className="member-share-result-actions"><button type="button" onClick={() => void copyText(url).then((ok) => setCopied(ok ? 'url' : 'failed'))}>{text.shareCopyUrl}</button>{result.secret && <button type="button" onClick={() => void copyText(result.secret ?? '').then((ok) => setCopied(ok ? 'secret' : 'failed'))}>{text.shareCopySecret}</button>}</div>
    {copied === 'url' || copied === 'secret' ? <p className="member-admin-notice">{text.shareUrlCopied}</p> : copied === 'failed' ? <p className="member-error">{text.shareCopyFailed}</p> : null}
    <div className="member-modal-actions"><button className="member-primary" type="button" onClick={onClose}>{text.shareClose}</button></div>
  </div></div>;
}

type ShareCreateModalProps = {
  text: LocaleText;
  locator: MemberContentLocator;
  targetLabel: string;
  onCancel: () => void;
  onCreated: (result: CreateShareResponse) => void;
};

export function ShareCreateModal({ text, locator, targetLabel, onCancel, onCreated }: ShareCreateModalProps) {
  const [options, setOptions] = useState<ShareOptionsState>(defaultShareOptions);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setLoading(true); setError('');
    try { onCreated(await createShare(buildCreateSharePayload(locator, options))); } catch (caught) { setError(describeError(caught)); } finally { setLoading(false); }
  }
  return <div className="member-modal-backdrop"><form className="member-modal member-admin-form" onSubmit={onSubmit}>
    <h2 className="member-admin-form-wide">{text.shareCreateTitle}</h2><p className="member-admin-form-wide member-modal-hint"><strong>{targetLabel}</strong></p><ShareOptionFields text={text} value={options} onChange={setOptions} />
    {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
    <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={onCancel}>{text.cancel}</button><button className="member-primary" type="submit" disabled={loading}>{text.shareSubmit}</button></div>
  </form></div>;
}

function CollaborationRow({ item, incoming, text, onRevoke }: { item: MemberCollaboration; incoming: boolean; text: LocaleText; onRevoke?: () => void }) {
  return <li className="member-collaboration-row"><strong>{item.folderName ?? item.displayName ?? item.path ?? text.myFiles}</strong><small>{incoming ? item.ownerName : item.recipientName} · {item.permission === 'viewer' ? text.readOnly : text.readWrite}</small>{!incoming && onRevoke && <button type="button" onClick={onRevoke}>{text.collaborationRevoke}</button>}</li>;
}

export default function MemberSharesPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [shares, setShares] = useState<SharePayload[]>([]);
  const [incoming, setIncoming] = useState<MemberCollaboration[]>([]);
  const [outgoing, setOutgoing] = useState<MemberCollaboration[]>([]);
  const [recipientID, setRecipientID] = useState('');
  const [rootPath, setRootPath] = useState('.');
  const [permission, setPermission] = useState<'viewer' | 'editor'>('viewer');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [shareLink, setShareLink] = useState<CreateShareResponse | null>(null);

  const load = useCallback(async () => {
    setLoading(true); setError('');
    try {
      const [shareResponse, received, sent] = await Promise.all([listShares(), listMemberCollaborations('incoming'), listMemberCollaborations('outgoing')]);
      setShares(shareResponse.items ?? []); setIncoming(received.items ?? []); setOutgoing(sent.items ?? []);
    } catch (caught) { setError(describeError(caught)); } finally { setLoading(false); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function createCollaboration(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setLoading(true); setError('');
    try { await createMemberCollaboration({ recipientId: recipientID.trim(), rootRelativePath: normalizeSharePath(rootPath), permission }); setRecipientID(''); setRootPath('.'); await load(); }
    catch (caught) { setError(describeError(caught)); setLoading(false); }
  }

  async function revokeCollaboration(id: string) {
    if (!window.confirm(text.collaborationRevoke)) return;
    setLoading(true); setError('');
    try { await deleteMemberCollaboration(id); await load(); } catch (caught) { setError(describeError(caught)); setLoading(false); }
  }

  return <div className="member-admin-workspace">
    <div className="member-heading"><div><h1>{text.sharesTitle}</h1><p>{text.sharesTitleDetail}</p></div><button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button></div>
    {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
    <section aria-labelledby="public-shares"><h2 id="public-shares">{text.sharesTitle}</h2>{loading && shares.length === 0 ? <div className="member-loading">{text.loading}</div> : shares.length === 0 ? <div className="member-empty">{text.shareListEmpty}</div> : <table className="member-admin-table"><thead><tr><th>{text.shareColumnTarget}</th><th>{text.shareColumnStatus}</th><th>{text.shareColumnExpires}</th><th>{text.actions}</th></tr></thead><tbody>{shares.map((share) => <tr key={share.id}><td><strong>{displaySharePath(share.relativePath, text)}</strong><small>{shareLocationLabel(share, text)}</small></td><td>{shareStatusLabel(share.status, text)}</td><td>{formatDate(share.expiresAt, locale, text)}</td><td><button className="member-table-action member-table-danger" type="button" onClick={() => void deleteShare(share.id).then(load)} disabled={loading}>{text.shareRevoke}</button></td></tr>)}</tbody></table>}</section>
    <section aria-labelledby="incoming-collaborations"><h2 id="incoming-collaborations">{text.sharedWithMe}</h2>{incoming.length === 0 ? <div className="member-empty">{text.noIncomingCollaborations}</div> : <ul className="member-collaboration-list">{incoming.map((item) => <CollaborationRow item={item} incoming text={text} key={item.id} />)}</ul>}</section>
    <section aria-labelledby="outgoing-collaborations"><h2 id="outgoing-collaborations">{text.outgoingCollaborations}</h2><form className="member-admin-inline-form" onSubmit={createCollaboration}><label>{text.collaborationRecipient}<input value={recipientID} onChange={(event) => setRecipientID(event.target.value)} required /></label><label>{text.collaborationRoot}<input value={rootPath} onChange={(event) => setRootPath(event.target.value)} required /></label><label>{text.collaborationPermission}<select value={permission} onChange={(event) => setPermission(event.target.value as 'viewer' | 'editor')}><option value="viewer">{text.readOnly}</option><option value="editor">{text.readWrite}</option></select></label><button className="member-primary" type="submit" disabled={loading}>{text.collaborationCreate}</button></form>{outgoing.length === 0 ? <div className="member-empty">{text.noOutgoingCollaborations}</div> : <ul className="member-collaboration-list">{outgoing.map((item) => <CollaborationRow item={item} incoming={false} text={text} key={item.id} onRevoke={() => void revokeCollaboration(item.id)} />)}</ul>}</section>
    {shareLink && <ShareCreatedResult text={text} result={shareLink} onClose={() => setShareLink(null)} />}
  </div>;
}
