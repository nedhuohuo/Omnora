import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  ApiError,
  getAdminUpdateStatus,
  isReauthenticationCanceled,
  rollbackAdminUpdate,
  type UpdateStatusPayload,
  uploadAdminUpdate,
} from '../api';
import { useMemberDialog } from './MemberDialog';
import { useRecentReauth } from './RecentReauthProvider';
import { type MemberLocale, localeMessages } from './i18n';

function describeError(error: unknown) {
  if (isReauthenticationCanceled(error)) return '';
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    return body?.error?.message || body?.error?.code || `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : 'Unknown error';
}

function stateLabel(state: string | undefined, text: typeof localeMessages[MemberLocale]) {
  const labels: Record<string, string> = {
    disabled: text.updateStateDisabled,
    built_in: text.updateStateBuiltIn,
    preparing: text.updateStatePreparing,
    pending_restart: text.updateStatePending,
    active: text.updateStateActive,
    rollback_pending: text.updateStateRollbackPending,
    failed: text.updateStateFailed,
  };
  return state ? (labels[state] ?? state) : '--';
}

function formatBytes(value: number | undefined) {
  if (!value || value <= 0) return '--';
  if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function ReleaseSummary({ title, release, locale, text }: { title: string; release: UpdateStatusPayload['current']; locale: MemberLocale; text: typeof localeMessages[MemberLocale] }) {
  if (!release) return null;
  const date = release.uploadedAt ? new Date(release.uploadedAt) : null;
  const uploadedAt = date && !Number.isNaN(date.valueOf())
    ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
    : '--';
  return (
    <div className="member-update-release">
      <h2>{title}</h2>
      <dl>
        <div><dt>{text.updateVersion}</dt><dd>{release.version}</dd></div>
        <div><dt>{text.updateTarget}</dt><dd>{release.targetOS}/{release.targetArch}</dd></div>
        {release.schemaVersion !== undefined && <div><dt>{text.updateSchema}</dt><dd>{release.schemaVersion}</dd></div>}
        {release.backupId && <div><dt>{text.updateBackup}</dt><dd>{release.backupId}</dd></div>}
        {release.archiveSizeBytes !== undefined && <div><dt>{text.updatePackageSize}</dt><dd>{formatBytes(release.archiveSizeBytes)}</dd></div>}
        <div><dt>{text.updateUploaded}</dt><dd>{uploadedAt}</dd></div>
      </dl>
      {release.archiveSha256 && <code className="member-update-hash">{text.updateSha256}: {release.archiveSha256}</code>}
    </div>
  );
}

export default function AdminUpdatesPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const { runSensitive } = useRecentReauth();
  const { confirm } = useMemberDialog();
  const [status, setStatus] = useState<UpdateStatusPayload | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [rollingBack, setRollingBack] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setStatus(await getAdminUpdateStatus());
      setError('');
    } catch (caught) {
      const detail = describeError(caught);
      if (detail) setError(detail);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = globalThis.setInterval(() => void load(), 5000);
    return () => globalThis.clearInterval(timer);
  }, [load]);

  async function onUpload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!file) return;
    if (!file.name.toLowerCase().endsWith('.tar.gz')) {
      setError(text.updatePackageInvalidName);
      return;
    }
    setUploading(true);
    setError('');
    setNotice('');
    try {
      const response = await runSensitive(() => uploadAdminUpdate(file));
      setNotice(response.message || text.updateQueued);
      setFile(null);
      await load();
    } catch (caught) {
      const detail = describeError(caught);
      if (detail) setError(detail);
    } finally {
      setUploading(false);
    }
  }

  async function onRollback() {
    const accepted = await confirm({
      title: text.updateRollbackConfirmTitle,
      description: text.updateRollbackConfirmDetail,
      confirmLabel: text.updateRollback,
      cancelLabel: text.cancel,
      tone: 'danger',
    });
    if (!accepted) return;
    setRollingBack(true);
    setError('');
    setNotice('');
    try {
      const response = await runSensitive(() => rollbackAdminUpdate());
      setNotice(response.message || text.updateRollbackQueued);
      await load();
    } catch (caught) {
      const detail = describeError(caught);
      if (detail) setError(detail);
    } finally {
      setRollingBack(false);
    }
  }

  const operationQueued = status?.state === 'preparing' || status?.state === 'pending_restart' || status?.state === 'rollback_pending';
  const busy = loading || uploading || rollingBack;
  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.updatesTitle}</h1><p>{text.updatesDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={busy}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {notice && <div className="member-admin-notice">{notice}</div>}
      <form className="member-admin-form member-update-form" onSubmit={(event) => void onUpload(event)}>
        <label className="member-admin-form-wide">{text.updatePackageLabel}
          <input type="file" accept=".tar.gz,application/gzip" onChange={(event) => setFile(event.currentTarget.files?.[0] ?? null)} disabled={busy || operationQueued} required />
        </label>
        <p className="member-admin-hint member-admin-form-wide">{text.updatePackageHint}</p>
        <div className="member-admin-form-actions member-admin-form-wide">
          <button className="member-primary" type="submit" disabled={busy || operationQueued || !file}>{uploading ? text.updateUploading : text.updateUpload}</button>
          <button className="member-modal-danger" type="button" onClick={() => void onRollback()} disabled={busy || operationQueued || !status?.current}>{text.updateRollback}</button>
        </div>
      </form>
      <div className="member-update-status">
        <div className="member-update-state"><span className={`member-status-dot ${status?.state === 'failed' ? 'danger' : status?.state === 'active' ? 'ok' : 'muted'}`} />{text.updateStateLabel}<strong>{stateLabel(status?.state, text)}</strong></div>
        {status?.failure && <p className="member-error member-update-failure">{text.updateFailure}: {status.failure}</p>}
        <ReleaseSummary title={text.updateCurrentRelease} release={status?.current} locale={locale} text={text} />
        <ReleaseSummary title={text.updatePreparingRelease} release={status?.preparing} locale={locale} text={text} />
        <ReleaseSummary title={text.updatePendingRelease} release={status?.pending} locale={locale} text={text} />
      </div>
    </div>
  );
}
