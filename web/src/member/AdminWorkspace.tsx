import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AdminMountListItem,
  type AdminSpacePayload,
  type AuditEventPayload,
  type JobPayload,
  ApiError,
  enqueueIndexJob,
  listAdminMounts,
  listAdminSpaces,
  listAuditEvents,
  listIndexJobs,
  registerAdminMount,
  runIndexJob,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';

export type AdminTab = 'mounts' | 'index-jobs' | 'audit';

type MountForm = {
  spaceId: string;
  displayName: string;
  rootPath: string;
  kind: 'external' | 'managed';
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
};

function describeError(error: unknown) {
  if (error instanceof ApiError) return `HTTP ${error.status}`;
  return error instanceof Error ? error.message : 'Unknown error';
}

function formatDate(value: string, locale: MemberLocale) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

function mountLabel(mount: AdminMountListItem) {
  return `${mount.space} / ${mount.name}`;
}

function isIndexableMount(mount: AdminMountListItem) {
  return mount.index === 'indexed' && mount.health === 'active';
}

function mountModeLabel(value: string, text: typeof localeMessages[MemberLocale]) {
  if (value === 'read-write') return text.readWrite;
  if (value === 'read-only') return text.readOnly;
  return value;
}

function indexLabel(value: string, text: typeof localeMessages[MemberLocale]) {
  if (value === 'indexed') return text.indexReady;
  if (value === 'not indexed') return text.indexDisabled;
  return value;
}

function statusLabel(value: string, text: typeof localeMessages[MemberLocale]) {
  const labels: Record<string, string> = {
    active: text.statusActive,
    queued: text.statusQueued,
    running: text.statusRunning,
    completed: text.statusCompleted,
    failed: text.statusFailed,
    paused: text.statusPaused,
    canceled: text.statusCanceled,
  };
  return labels[value] ?? value;
}

function jobKindLabel(value: string, text: typeof localeMessages[MemberLocale]) {
  return value === 'catalog_scan' ? text.catalogScan : value;
}

export default function AdminWorkspace({ tab, locale }: { tab: AdminTab; locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [spaces, setSpaces] = useState<AdminSpacePayload[]>([]);
  const [mounts, setMounts] = useState<AdminMountListItem[]>([]);
  const [jobs, setJobs] = useState<JobPayload[]>([]);
  const [events, setEvents] = useState<AuditEventPayload[]>([]);
  const [mountForm, setMountForm] = useState<MountForm>({ spaceId: '', displayName: '', rootPath: '', kind: 'external', mode: 'read_write', indexEnabled: true });
  const [selectedMountId, setSelectedMountId] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [operationComplete, setOperationComplete] = useState(false);

  const loadMountData = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [spaceResponse, mountResponse] = await Promise.all([listAdminSpaces(), listAdminMounts()]);
      setSpaces(spaceResponse.items);
      setMounts(mountResponse.items);
      setMountForm((current) => ({ ...current, spaceId: spaceResponse.items.some((space) => space.id === current.spaceId) ? current.spaceId : (spaceResponse.items[0]?.id ?? '') }));
      setSelectedMountId((current) => mountResponse.items.some((mount) => mount.id === current) ? current : (mountResponse.items[0]?.id ?? ''));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadJobs = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [jobResponse, mountResponse] = await Promise.all([listIndexJobs(), listAdminMounts()]);
      setJobs(jobResponse.items ?? []);
      setMounts(mountResponse.items);
      const indexableMounts = mountResponse.items.filter(isIndexableMount);
      setSelectedMountId((current) => indexableMounts.some((mount) => mount.id === current) ? current : (indexableMounts[0]?.id ?? ''));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadAudit = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAuditEvents();
      setEvents(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (tab === 'mounts') void loadMountData();
    if (tab === 'index-jobs') void loadJobs();
    if (tab === 'audit') void loadAudit();
  }, [loadAudit, loadJobs, loadMountData, tab]);

  async function onCreateMount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await registerAdminMount(mountForm);
      setMountForm((current) => ({ ...current, displayName: '', rootPath: '' }));
      setOperationComplete(true);
      await loadMountData();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onQueueIndex(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedMountId) return;
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await enqueueIndexJob(selectedMountId);
      setOperationComplete(true);
      await loadJobs();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onRunJob(jobId: string) {
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await runIndexJob(jobId);
      setOperationComplete(true);
      await loadJobs();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  const refresh = tab === 'mounts' ? loadMountData : tab === 'index-jobs' ? loadJobs : loadAudit;
  const indexableMounts = mounts.filter(isIndexableMount);

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div>
          <h1>{tab === 'mounts' ? text.mountManagement : tab === 'index-jobs' ? text.indexJobs : text.auditLog}</h1>
          <p>{tab === 'mounts' ? text.mountManagementDetail : tab === 'index-jobs' ? text.indexJobsDetail : text.auditLogDetail}</p>
        </div>
        <button className="member-secondary-action" type="button" onClick={() => void refresh()} disabled={loading}>{text.refresh}</button>
      </div>

      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {operationComplete && <p className="member-admin-notice">{text.adminOperationComplete}</p>}

      {tab === 'mounts' && <>
        <form className="member-admin-form" onSubmit={onCreateMount}>
          <label>{text.mountSpace}<select value={mountForm.spaceId} onChange={(event) => setMountForm({ ...mountForm, spaceId: event.target.value })} required><option value="" disabled>{text.mountSpace}</option>{spaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}</select></label>
          <label>{text.mountName}<input value={mountForm.displayName} onChange={(event) => setMountForm({ ...mountForm, displayName: event.target.value })} required /></label>
          <label className="member-admin-form-wide">{text.mountRoot}<input value={mountForm.rootPath} onChange={(event) => setMountForm({ ...mountForm, rootPath: event.target.value })} placeholder="/srv/omnora/data" required /></label>
          <label>{text.mountKind}<select value={mountForm.kind} onChange={(event) => setMountForm({ ...mountForm, kind: event.target.value as MountForm['kind'] })}><option value="external">{text.externalMount}</option><option value="managed">{text.managedMount}</option></select></label>
          <label>{text.mountMode}<select value={mountForm.mode} onChange={(event) => setMountForm({ ...mountForm, mode: event.target.value as MountForm['mode'] })}><option value="read_write">{text.readWrite}</option><option value="read_only">{text.readOnly}</option></select></label>
          <label className="member-admin-checkbox"><input type="checkbox" checked={mountForm.indexEnabled} onChange={(event) => setMountForm({ ...mountForm, indexEnabled: event.target.checked })} />{text.enableIndex}</label>
          <div className="member-admin-form-actions"><button className="member-primary" type="submit" disabled={loading || !mountForm.spaceId}>{text.createMount}</button></div>
        </form>
        {loading ? <div className="member-loading">{text.loading}</div> : mounts.length === 0 ? <div className="member-empty">{spaces.length === 0 ? text.noAdminSpaces : text.noAdminMounts}</div> : <table className="member-admin-table"><thead><tr><th>{text.mountName}</th><th>{text.mountSpace}</th><th>{text.mountMode}</th><th>{text.enableIndex}</th><th>{text.status}</th></tr></thead><tbody>{mounts.map((mount) => <tr key={mount.id}><td>{mount.name}</td><td>{mount.space}</td><td>{mountModeLabel(mount.mode, text)}</td><td>{indexLabel(mount.index, text)}</td><td>{statusLabel(mount.health, text)}</td></tr>)}</tbody></table>}
      </>}

      {tab === 'index-jobs' && <>
        {indexableMounts.length === 0 ? <div className="member-empty">{text.noIndexableMounts}</div> : <form className="member-admin-inline-form" onSubmit={onQueueIndex}><label>{text.selectMount}<select value={selectedMountId} onChange={(event) => setSelectedMountId(event.target.value)} required>{indexableMounts.map((mount) => <option key={mount.id} value={mount.id}>{mountLabel(mount)}</option>)}</select></label><button className="member-primary" type="submit" disabled={loading || !selectedMountId}>{text.queueIndex}</button></form>}
        {loading ? <div className="member-loading">{text.loading}</div> : jobs.length === 0 ? <div className="member-empty">{text.noIndexJobs}</div> : <table className="member-admin-table"><thead><tr><th>{text.job}</th><th>{text.status}</th><th>{text.attempts}</th><th>{text.updated}</th><th>{text.actions}</th></tr></thead><tbody>{jobs.map((job) => <tr key={job.id}><td>{jobKindLabel(job.kind ?? '--', text)}<small>{job.id}</small></td><td>{statusLabel(job.status ?? '--', text)}</td><td>{job.attempts ?? 0}/{job.maxAttempts ?? 0}</td><td>{formatDate(job.updatedAt ?? job.createdAt ?? '', locale)}</td><td>{job.status === 'queued' && job.id ? <button className="member-table-action" type="button" onClick={() => void onRunJob(job.id!)} disabled={loading}>{text.run}</button> : '--'}</td></tr>)}</tbody></table>}
      </>}

      {tab === 'audit' && (loading ? <div className="member-loading">{text.loading}</div> : events.length === 0 ? <div className="member-empty">{text.noAuditEvents}</div> : <table className="member-admin-table"><thead><tr><th>{text.time}</th><th>{text.actor}</th><th>{text.event}</th><th>{text.target}</th></tr></thead><tbody>{events.map((event, index) => <tr key={`${event.occurredAt}-${event.action}-${index}`}><td>{formatDate(event.occurredAt, locale)}</td><td>{event.actor}</td><td>{event.action}</td><td>{event.targetType}{event.targetId ? ` / ${event.targetId}` : ''}</td></tr>)}</tbody></table>)}
    </div>
  );
}
