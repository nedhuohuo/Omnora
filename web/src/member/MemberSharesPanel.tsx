import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  ApiError,
  buildShareURL,
  type CreateShareResponse,
  createShare,
  deleteShare,
  listDirectoryChildren,
  listMounts,
  listShares,
  listSpaces,
  type SharePayload,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { formatDirectoryChildren, type MemberDirectoryEntry, type MemberMount, type MemberSpace } from './types';

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

function browseCrumbs(path: string) {
  const normalized = normalizeSharePath(path);
  if (normalized === '.') return [];
  return normalized.split('/').filter(Boolean);
}

async function copyToClipboard(value: string) {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    return false;
  }
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
                  <span className="member-file-icon dir">DIR</span>
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
              <span className="member-file-icon file">{entry.name.split('.').pop()?.slice(0, 3).toUpperCase() || 'FILE'}</span>
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
  const [copied, setCopied] = useState<'url' | 'secret' | null>(null);

  return (
    <div className="member-modal-backdrop">
      <div className="member-modal member-share-result">
        <h2>{text.shareCreatedTitle}</h2>
        <p className="member-modal-hint">{text.shareCreatedHint}</p>
        <code className="member-share-url">{url}</code>
        <div className="member-share-result-actions">
          <button type="button" onClick={() => void copyToClipboard(url).then((ok) => setCopied(ok ? 'url' : null))}>{text.shareCopyUrl}</button>
          {result.secret && <button type="button" onClick={() => void copyToClipboard(result.secret ?? '').then((ok) => setCopied(ok ? 'secret' : null))}>{text.shareCopySecret}</button>}
        </div>
        {copied && <p className="member-admin-notice">{text.shareUrlCopied}</p>}
        <div><button className="member-primary" type="button" onClick={onClose}>{text.shareClose}</button></div>
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
  const [spaces, setSpaces] = useState<MemberSpace[]>([]);
  const [mounts, setMounts] = useState<MemberMount[]>([]);
  const [formSpaceId, setFormSpaceId] = useState('');
  const [formMountId, setFormMountId] = useState('');
  const [formPath, setFormPath] = useState('.');
  const [options, setOptions] = useState<ShareOptionsState>(defaultShareOptions);
  const [creating, setCreating] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [createdResult, setCreatedResult] = useState<CreateShareResponse | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<SharePayload | null>(null);

  const managerSpaces = spaces.filter((space) => space.role === 'manager');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [shareResponse, spaceResponse] = await Promise.all([listShares(), listSpaces()]);
      setShares(shareResponse.items ?? []);
      setSpaces(spaceResponse.items);
      setFormSpaceId((current) => (spaceResponse.items.some((space) => space.id === current) ? current : (spaceResponse.items.find((space) => space.role === 'manager')?.id ?? '')));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!formSpaceId) {
      setMounts([]);
      return;
    }
    void listMounts(formSpaceId).then((response) => {
      setMounts(response.items);
      setFormMountId((current) => (response.items.some((mount) => mount.id === current) ? current : (response.items[0]?.id ?? '')));
    }).catch(() => setMounts([]));
  }, [formSpaceId]);

  useEffect(() => {
    setFormPath('.');
  }, [formSpaceId, formMountId]);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!formSpaceId || !formMountId) return;
    setCreating(true);
    setError('');
    try {
      const result = await createShare(buildCreateSharePayload(formSpaceId, formMountId, formPath, options));
      setCreatedResult(result);
      setFormOpen(false);
      setFormPath('.');
      setOptions(defaultShareOptions);
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
          {managerSpaces.length > 0 && <button className="member-primary" type="button" onClick={() => setFormOpen(true)}>{text.shareCreate}</button>}
        </div>
      </div>

      {managerSpaces.length === 0 && <p className="member-readonly">{text.shareManagerOnlyHint}</p>}
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

      {loading ? <div className="member-loading">{text.loading}</div> : shares.length === 0 ? <div className="member-empty">{text.shareListEmpty}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.shareColumnTarget}</th><th>{text.shareColumnStatus}</th><th>{text.shareColumnExpires}</th><th>{text.shareColumnVisits}</th><th>{text.shareColumnDownloads}</th><th>{text.actions}</th></tr></thead>
          <tbody>{shares.map((share) => (
            <tr key={share.id}>
              <td><strong>{share.relativePath}</strong><small>{share.spaceId} · {share.mountId}</small></td>
              <td>{shareStatusLabel(share.status, text)}</td>
              <td>{formatDate(share.expiresAt, locale, text)}</td>
              <td>{share.usedVisits ?? 0}{share.maxVisits ? ` / ${share.maxVisits}` : ''}</td>
              <td>{share.usedDownloads ?? 0}{share.maxDownloads ? ` / ${share.maxDownloads}` : ''}</td>
              <td><button className="member-table-action member-table-danger" type="button" onClick={() => setRevokeTarget(share)} disabled={loading}>{text.shareRevoke}</button></td>
            </tr>
          ))}</tbody>
        </table>
      )}

      {formOpen && (
        <div className="member-modal-backdrop">
          <form className="member-modal member-admin-form" onSubmit={onSubmit}>
            <h2 className="member-admin-form-wide">{text.shareCreateTitle}</h2>
            <label>{text.shareTargetSpace}<select value={formSpaceId} onChange={(event) => setFormSpaceId(event.target.value)} required>{managerSpaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}</select></label>
            <label>{text.shareTargetMount}<select value={formMountId} onChange={(event) => setFormMountId(event.target.value)} required>{mounts.map((mount) => <option key={mount.id} value={mount.id}>{mount.name}</option>)}</select></label>
            <ShareTargetPicker key={`${formSpaceId}:${formMountId}`} text={text} spaceId={formSpaceId} mountId={formMountId} selectedPath={formPath} onSelect={setFormPath} />
            <ShareOptionFields text={text} value={options} onChange={setOptions} />
            {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
            <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={() => setFormOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={creating || !formMountId}>{text.shareSubmit}</button></div>
          </form>
        </div>
      )}

      {createdResult && <ShareCreatedResult text={text} result={createdResult} onClose={() => setCreatedResult(null)} />}

      {revokeTarget && (
        <div className="member-modal-backdrop">
          <div className="member-modal">
            <h2>{text.shareRevokeConfirmTitle}</h2>
            <p className="member-modal-hint">{text.shareRevokeConfirmDetail}</p>
            <p className="member-modal-hint"><strong>{revokeTarget.relativePath}</strong></p>
            <div><button type="button" onClick={() => setRevokeTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="button" onClick={() => void onRevoke()} disabled={loading}>{text.shareRevoke}</button></div>
          </div>
        </div>
      )}
    </div>
  );
}
