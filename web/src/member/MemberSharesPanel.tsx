import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  ApiError,
  buildShareURL,
  type CreateShareResponse,
  createShare,
  deleteShare,
  listDirectoryChildren,
  listShares,
  type SharePayload,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { formatDirectoryChildren, type MemberDirectoryEntry, type MemberMount } from './types';
import { copyText } from './clipboard';
import { joinReadableLabels } from './displayLabels';
import FileTypeIcon from './FileTypeIcon';

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
    creator_lost_manager: text.shareStatusCreatorLostManager,
    target_moved_or_replaced: text.shareStatusTargetMoved,
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

function shareLocationLabel(share: SharePayload) {
  return joinReadableLabels([share.spaceName, share.mountName]);
}

function browseCrumbs(path: string) {
  const normalized = normalizeSharePath(path);
  if (normalized === '.') return [];
  return normalized.split('/').filter(Boolean);
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

export function ShareOptionFields({
  text,
  value,
  onChange,
}: {
  text: LocaleText;
  value: ShareOptionsState;
  onChange: (next: ShareOptionsState) => void;
}) {
  return (
    <>
      <label>{text.sharePasswordOptional}<input type="password" value={value.password} onChange={(event) => onChange({ ...value, password: event.target.value })} autoComplete="new-password" /></label>
      <label>{text.shareExpiresAt}<input type="datetime-local" value={value.expiresAt} onChange={(event) => onChange({ ...value, expiresAt: event.target.value })} /><small className="member-path-hint">{text.shareNeverExpires}</small></label>
      <label className="member-admin-checkbox"><input type="checkbox" checked={value.allowPreview} onChange={(event) => onChange({ ...value, allowPreview: event.target.checked })} />{text.shareAllowPreview}</label>
      <label className="member-admin-checkbox"><input type="checkbox" checked={value.allowDownload} onChange={(event) => onChange({ ...value, allowDownload: event.target.checked })} />{text.shareAllowDownload}</label>
    </>
  );
}

export function buildCreateSharePayload(spaceId: string, mountId: string, relativePath: string, options: ShareOptionsState) {
  const expiresAtIso = options.expiresAt ? new Date(options.expiresAt).toISOString() : undefined;
  return {
    spaceId,
    mountId,
    relativePath: normalizeSharePath(relativePath),
    password: options.password.trim() || undefined,
    allowPreview: options.allowPreview,
    allowDownload: options.allowDownload,
    expiresAt: expiresAtIso,
  };
}

export function resolveShareFragment(result: CreateShareResponse) {
  if (result.fragment) return result.fragment;
  if (result.publicId && result.secret) return `${result.publicId}.${result.secret}`;
  return '';
}

function ShareTargetPicker({
  text,
  spaceId,
  mountId,
  selectedPath,
  onSelect,
}: {
  text: LocaleText;
  spaceId: string;
  mountId: string;
  selectedPath: string;
  onSelect: (path: string) => void;
}) {
  const [browsePath, setBrowsePath] = useState('.');
  const [entries, setEntries] = useState<MemberDirectoryEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!spaceId || !mountId) {
      setEntries([]);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setError('');
    void listDirectoryChildren(spaceId, mountId, browsePath, controller.signal)
      .then((payload) => {
        const listing = formatDirectoryChildren(payload);
        setEntries(listing.entries);
      })
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return;
        setEntries([]);
        setError(describeError(caught));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [spaceId, mountId, browsePath]);

  const crumbs = browseCrumbs(browsePath);
  const selected = normalizeSharePath(selectedPath);

  return (
    <div className="member-share-picker member-admin-form-wide">
      <span>{text.shareTargetPath}</span>
      <div className="member-share-picker-toolbar">
        <div className="member-share-picker-crumbs">
          <button type="button" onClick={() => setBrowsePath('.')}>{text.shareBrowseRoot}</button>
          {crumbs.map((part, index) => {
            const path = crumbs.slice(0, index + 1).join('/');
            const isLast = index === crumbs.length - 1;
            return (
              <span key={path}>
                <b>/</b>
                {isLast ? <strong>{part}</strong> : <button type="button" onClick={() => setBrowsePath(path)}>{part}</button>}
              </span>
            );
          })}
        </div>
        <div className="member-share-picker-actions">
          <button type="button" onClick={() => onSelect(browsePath)}>{text.shareSelectCurrentFolder}</button>
        </div>
      </div>
      <p className="member-path-hint">{text.shareBrowseHint}</p>
      <p className="member-share-picker-selected">{text.shareSelectedTarget}: {displaySharePath(selected, text)}</p>
      <div className="member-share-picker-list" role="listbox" aria-label={text.shareTargetPath}>
        {loading ? <div className="member-share-picker-loading">{text.loading}</div> : error ? <div className="member-share-picker-empty">{error}</div> : entries.length === 0 ? <div className="member-share-picker-empty">{text.shareBrowseEmpty}</div> : entries.map((entry) => {
          const entryPath = normalizeSharePath(entry.relativePath);
          const isSelected = selected === entryPath;
          if (entry.kind === 'dir') {
            return (
              <div key={`dir-${entryPath}`} className={`member-share-picker-row${isSelected ? ' selected' : ''}`}>
                <button type="button" className="member-share-picker-item" onClick={() => setBrowsePath(entryPath)}>
                  <FileTypeIcon kind="dir" name={entry.name} className="member-file-icon dir" />
                  <span>{entry.name}</span>
                </button>
                <button type="button" className="member-share-picker-select" onClick={() => onSelect(entryPath)}>{text.shareSelectItem}</button>
              </div>
            );
          }
          return (
            <button
              key={`file-${entryPath}`}
              type="button"
              className={`member-share-picker-item${isSelected ? ' selected' : ''}`}
              role="option"
              aria-selected={isSelected}
              onClick={() => onSelect(entryPath)}
            >
              <FileTypeIcon kind="file" name={entry.name} className="member-file-icon file" />
              <span>{entry.name}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

export function ShareCreatedResult({ text, result, onClose }: { text: LocaleText; result: CreateShareResponse; onClose: () => void }) {
  const fragment = resolveShareFragment(result);
  const url = fragment ? buildShareURL(fragment) : '';
  const [copied, setCopied] = useState<'url' | 'secret' | 'failed' | null>(null);

  return (
    <div className="member-modal-backdrop">
      <div className="member-modal member-share-result">
        <h2>{text.shareCreatedTitle}</h2>
        <p className="member-modal-hint">{text.shareCreatedHint}</p>
        <code className="member-share-url">{url}</code>
        <div className="member-share-result-actions">
          <button type="button" onClick={() => void copyText(url).then((ok) => setCopied(ok ? 'url' : 'failed'))}>{text.shareCopyUrl}</button>
          {result.secret && <button type="button" onClick={() => void copyText(result.secret ?? '').then((ok) => setCopied(ok ? 'secret' : 'failed'))}>{text.shareCopySecret}</button>}
        </div>
        {copied === 'url' || copied === 'secret' ? <p className="member-admin-notice">{text.shareUrlCopied}</p> : copied === 'failed' ? <p className="member-error">{text.shareCopyFailed}</p> : null}
        <div className="member-modal-actions"><button className="member-primary" type="button" onClick={onClose}>{text.shareClose}</button></div>
      </div>
    </div>
  );
}

function ShareLinkViewer({ text, share, onClose }: { text: LocaleText; share: SharePayload; onClose: () => void }) {
  const url = share.fragment ? buildShareURL(share.fragment) : '';
  const [copied, setCopied] = useState<'url' | 'failed' | null>(null);

  return (
    <div className="member-modal-backdrop">
      <div className="member-modal member-share-result">
        <h2>{text.shareLinkTitle}</h2>
        <p className="member-modal-hint"><strong>{displaySharePath(share.relativePath, text)}</strong></p>
        {url ? (
          <>
            <p className="member-modal-hint">{text.shareLinkHint}</p>
            <code className="member-share-url">{url}</code>
            <div className="member-share-result-actions">
              <button type="button" onClick={() => void copyText(url).then((ok) => setCopied(ok ? 'url' : 'failed'))}>{text.shareCopyUrl}</button>
            </div>
            {copied === 'url' ? <p className="member-admin-notice">{text.shareUrlCopied}</p> : copied === 'failed' ? <p className="member-error">{text.shareCopyFailed}</p> : null}
          </>
        ) : (
          <p className="member-error">{text.shareLinkUnavailable}</p>
        )}
        <div className="member-modal-actions"><button className="member-primary" type="button" onClick={onClose}>{text.shareClose}</button></div>
      </div>
    </div>
  );
}

type ShareCreateModalProps = {
  text: LocaleText;
  spaceId: string;
  mountId: string;
  relativePath: string;
  targetLabel: string;
  onCancel: () => void;
  onCreated: (result: CreateShareResponse) => void;
};

export function ShareCreateModal({ text, spaceId, mountId, relativePath, targetLabel, onCancel, onCreated }: ShareCreateModalProps) {
  const [options, setOptions] = useState<ShareOptionsState>(defaultShareOptions);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      const result = await createShare(buildCreateSharePayload(spaceId, mountId, relativePath, options));
      onCreated(result);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="member-modal-backdrop">
      <form className="member-modal member-admin-form" onSubmit={onSubmit}>
        <h2 className="member-admin-form-wide">{text.shareCreateTitle}</h2>
        <p className="member-admin-form-wide member-modal-hint"><strong>{targetLabel}</strong></p>
        <ShareOptionFields text={text} value={options} onChange={setOptions} />
        {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
        <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={onCancel}>{text.cancel}</button><button className="member-primary" type="submit" disabled={loading}>{text.shareSubmit}</button></div>
      </form>
    </div>
  );
}

export default function MemberSharesPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [shares, setShares] = useState<SharePayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [revokeTarget, setRevokeTarget] = useState<SharePayload | null>(null);
  const [linkTarget, setLinkTarget] = useState<SharePayload | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const shareResponse = await listShares();
      setShares(shareResponse.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onRevoke() {
    if (!revokeTarget) return;
    setLoading(true);
    setError('');
    try {
      await deleteShare(revokeTarget.id);
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
        <div><h1>{text.sharesTitle}</h1><p>{text.sharesTitleDetail}</p></div>
        <div className="member-admin-table-actions">
          <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
        </div>
      </div>

      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

      {loading ? <div className="member-loading">{text.loading}</div> : shares.length === 0 ? <div className="member-empty">{text.shareListEmpty}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.shareColumnTarget}</th><th>{text.shareColumnStatus}</th><th>{text.shareColumnExpires}</th><th>{text.shareColumnVisits}</th><th>{text.shareColumnDownloads}</th><th>{text.actions}</th></tr></thead>
          <tbody>{shares.map((share) => {
            const location = shareLocationLabel(share);
            return (
              <tr key={share.id}>
                <td><strong>{displaySharePath(share.relativePath, text)}</strong>{location && <small>{location}</small>}</td>
                <td>{shareStatusLabel(share.status, text)}</td>
                <td>{formatDate(share.expiresAt, locale, text)}</td>
                <td>{share.usedVisits ?? 0}{share.maxVisits ? ` / ${share.maxVisits}` : ''}</td>
                <td>{share.usedDownloads ?? 0}{share.maxDownloads ? ` / ${share.maxDownloads}` : ''}</td>
                <td><div className="member-admin-table-actions">
                  <button className="member-table-action" type="button" onClick={() => setLinkTarget(share)} disabled={loading}>{text.shareViewLink}</button>
                  <button className="member-table-action member-table-danger" type="button" onClick={() => setRevokeTarget(share)} disabled={loading}>{text.shareRevoke}</button>
                </div></td>
              </tr>
            );
          })}</tbody>
        </table>
      )}

      {linkTarget && <ShareLinkViewer text={text} share={linkTarget} onClose={() => setLinkTarget(null)} />}

      {revokeTarget && (
        <div className="member-modal-backdrop">
          <div className="member-modal">
            <h2>{text.shareRevokeConfirmTitle}</h2>
            <p className="member-modal-hint">{text.shareRevokeConfirmDetail}</p>
            <p className="member-modal-hint"><strong>{revokeTarget.relativePath}</strong></p>
            <div className="member-modal-actions"><button type="button" onClick={() => setRevokeTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="button" onClick={() => void onRevoke()} disabled={loading}>{text.shareRevoke}</button></div>
          </div>
        </div>
      )}
    </div>
  );
}
