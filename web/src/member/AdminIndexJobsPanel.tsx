import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AdminMountListItem,
  type JobPayload,
  ApiError,
  enqueueIndexJob,
  listAdminMounts,
  listIndexJobs,
  runIndexJob,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    return body?.error?.message || body?.error?.code || `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : String(error);
}

function statusLabel(status: string | undefined, text: typeof localeMessages[MemberLocale]) {
  const labels: Record<string, string> = {
    queued: text.statusQueued,
    running: text.statusRunning,
    completed: text.statusCompleted,
    failed: text.statusFailed,
    paused: text.statusPaused,
    canceled: text.statusCanceled,
  };
  return labels[status ?? ''] ?? status ?? '--';
}

export default function AdminIndexJobsPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [mounts, setMounts] = useState<AdminMountListItem[]>([]);
  const [jobs, setJobs] = useState<JobPayload[]>([]);
  const [selectedMountId, setSelectedMountId] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [jobResponse, mountResponse] = await Promise.all([listIndexJobs(), listAdminMounts()]);
      setJobs(jobResponse.items ?? []);
      setMounts(mountResponse.items ?? []);
      const indexable = (mountResponse.items ?? []).filter((mount) => mount.indexEnabled && mount.status === 'active');
      setSelectedMountId((current) => indexable.some((mount) => mount.id === current) ? current : (indexable[0]?.id ?? ''));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onQueue(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedMountId) return;
    setLoading(true);
    setError('');
    setNotice('');
    try {
      await enqueueIndexJob(selectedMountId);
      setNotice(text.adminOperationComplete);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onRun(jobId: string) {
    setLoading(true);
    setError('');
    setNotice('');
    try {
      await runIndexJob(jobId);
      setNotice(text.adminOperationComplete);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  const indexableMounts = mounts.filter((mount) => mount.indexEnabled && mount.status === 'active');
  return (
    <div className="member-admin-workspace">
      <div className="member-heading"><div><h1>{text.indexJobs}</h1><p>{text.indexJobsDetail}</p></div><button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button></div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {notice && <p className="member-admin-notice">{notice}</p>}
      {indexableMounts.length === 0 ? <div className="member-empty">{text.noIndexableMounts}</div> : <form className="member-admin-inline-form" onSubmit={onQueue}><label>{text.selectMount}<select value={selectedMountId} onChange={(event) => setSelectedMountId(event.target.value)} required>{indexableMounts.map((mount) => <option key={mount.id} value={mount.id}>{mount.displayName}</option>)}</select></label><button className="member-primary" type="submit" disabled={loading || !selectedMountId}>{text.queueIndex}</button></form>}
      {loading && jobs.length === 0 ? <div className="member-loading">{text.loading}</div> : jobs.length === 0 ? <div className="member-empty">{text.noIndexJobs}</div> : <table className="member-admin-table"><thead><tr><th>{text.job}</th><th>{text.status}</th><th>{text.attempts}</th><th>{text.updated}</th><th>{text.actions}</th></tr></thead><tbody>{jobs.map((job) => <tr key={job.id}><td>{job.mountName || '--'}<small>{job.kind || '--'}</small></td><td>{statusLabel(job.status, text)}</td><td>{job.attempts ?? 0}/{job.maxAttempts ?? 0}</td><td>{job.updatedAt || job.createdAt || '--'}</td><td>{job.status === 'queued' && job.id ? <button className="member-table-action" type="button" onClick={() => void onRun(job.id!)} disabled={loading}>{text.run}</button> : '--'}</td></tr>)}</tbody></table>}
    </div>
  );
}
