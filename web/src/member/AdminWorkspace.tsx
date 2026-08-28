import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AdminMountListItem,
  type AdminRouteGroupItem,
  type AuditEventPayload,
  type JobPayload,
  ApiError,
  enqueueIndexJob,
  listAdminMounts,
  listAdminRouteGroups,
  listAuditEvents,
  listIndexJobs,
  runIndexJob,
  updateAdminRouteGroup,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { readableLabel } from './displayLabels';
import AdminStorageLocationsPanel from '../admin/storage/AdminStorageLocationsPanel';
import {
  AdminBackupsPanel,
  AdminOverviewPanel,
  AdminShareGovernancePanel,
  AdminSpacesPanel,
  AdminTokenGovernancePanel,
  AdminUsersPanel,
} from './AdminPanels';

export type AdminTab =
  | 'overview'
  | 'users'
  | 'spaces'
  | 'storage'
  | 'mounts'
  | 'index-jobs'
  | 'route-groups'
  | 'share-governance'
  | 'token-governance'
  | 'backups'
  | 'audit';

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

function statusLabel(value: string, text: typeof localeMessages[MemberLocale]) {
  const labels: Record<string, string> = {
    active: text.statusActive,
    unavailable: text.statusUnavailable,
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

function jobTargetLabel(job: JobPayload) {
  const spaceName = readableLabel(job.spaceName);
  const mountName = readableLabel(job.mountName);
  if (spaceName && mountName) return `${spaceName} / ${mountName}`;
  return mountName || spaceName || '';
}

function routeGroupLabel(id: string, text: typeof localeMessages[MemberLocale]) {
  const labels: Record<string, string> = {
    member_web: text.routeGroupMemberWeb,
    admin_web: text.routeGroupAdminWeb,
    share: text.routeGroupShare,
    rest: text.routeGroupRest,
    mcp: text.routeGroupMcp,
    openapi: text.routeGroupOpenapi,
  };
  return labels[id] ?? id;
}

function routeGroupDetail(id: string, text: typeof localeMessages[MemberLocale]) {
  const details: Record<string, string> = {
    member_web: text.routeDescMemberWeb,
    admin_web: text.routeDescAdminWeb,
    share: text.routeDescShare,
    rest: text.routeDescRest,
    mcp: text.routeDescMcp,
    openapi: text.routeDescOpenapi,
  };
  return details[id] ?? '';
}

function auditActorLabel(event: AuditEventPayload) {
  const label = readableLabel(event.actorLabel);
  const displayName = readableLabel(event.actorDisplayName);
  const email = readableLabel(event.actorEmail);
  const actor = event.actor === 'system' ? 'system' : readableLabel(event.actor);
  return {
    primary: label || displayName || email || actor || '--',
    secondary: email && email !== label && email !== displayName ? email : '',
  };
}

function auditTargetLabel(event: AuditEventPayload) {
  const label = readableLabel(event.targetLabel) || readableLabel(event.targetId);
  return label ? `${event.targetType} / ${label}` : event.targetType;
}

type AdminWorkspaceProps = {
  tab: AdminTab;
  locale: MemberLocale;
  resourceId?: string;
  onResourceChange?: (resourceId: string) => void;
};

export default function AdminWorkspace({ tab, locale, resourceId, onResourceChange }: AdminWorkspaceProps) {
  const text = localeMessages[locale];
  const [mounts, setMounts] = useState<AdminMountListItem[]>([]);
  const [jobs, setJobs] = useState<JobPayload[]>([]);
  const [routeGroups, setRouteGroups] = useState<AdminRouteGroupItem[]>([]);
  const [events, setEvents] = useState<AuditEventPayload[]>([]);
  const [selectedMountId, setSelectedMountId] = useState('');
  const [loading, setLoading] = useState(false);
  const [pendingGroupId, setPendingGroupId] = useState('');
  const [error, setError] = useState('');
  const [operationComplete, setOperationComplete] = useState(false);

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

  const loadRouteGroups = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminRouteGroups();
      setRouteGroups(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (tab === 'index-jobs') void loadJobs();
    if (tab === 'route-groups') void loadRouteGroups();
    if (tab === 'audit') void loadAudit();
  }, [loadAudit, loadJobs, loadRouteGroups, tab]);

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

  async function onToggleRouteGroup(group: AdminRouteGroupItem) {
    const nextExposed = !group.exposed;
    if (!nextExposed && (group.id === 'admin_web' || group.id === 'rest')) {
      if (!window.confirm(text.routeLockoutWarn)) return;
    }
    setPendingGroupId(group.id);
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      const updated = await updateAdminRouteGroup(group.id, nextExposed);
      setRouteGroups((current) => current.map((item) => (item.id === updated.id ? updated : item)));
      setOperationComplete(true);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingGroupId('');
      setLoading(false);
    }
  }

  if (tab === 'overview') return <AdminOverviewPanel locale={locale} />;
  if (tab === 'users') return <AdminUsersPanel locale={locale} />;
  if (tab === 'spaces') return <AdminSpacesPanel locale={locale} />;
  if (tab === 'share-governance') return <AdminShareGovernancePanel locale={locale} />;
  if (tab === 'token-governance') return <AdminTokenGovernancePanel locale={locale} />;
  if (tab === 'backups') return <AdminBackupsPanel locale={locale} />;

  if (tab === 'storage' || tab === 'mounts') {
    return (
      <AdminStorageLocationsPanel
        locale={locale}
        mountId={resourceId}
        onMountChange={onResourceChange}
      />
    );
  }

  const refresh = tab === 'index-jobs' ? loadJobs : tab === 'route-groups' ? loadRouteGroups : loadAudit;
  const indexableMounts = mounts.filter(isIndexableMount);
  const heading = tab === 'index-jobs' ? text.indexJobs : tab === 'route-groups' ? text.routeGroups : text.auditLog;
  const headingDetail = tab === 'index-jobs' ? text.indexJobsDetail : tab === 'route-groups' ? text.routeGroupsDetail : text.auditLogDetail;

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div>
          <h1>{heading}</h1>
          <p>{headingDetail}</p>
        </div>
        <button className="member-secondary-action" type="button" onClick={() => void refresh()} disabled={loading}>{text.refresh}</button>
      </div>

      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {operationComplete && <p className="member-admin-notice">{text.adminOperationComplete}</p>}

      {tab === 'index-jobs' && <>
        {indexableMounts.length === 0 ? <div className="member-empty">{text.noIndexableMounts}</div> : <form className="member-admin-inline-form" onSubmit={onQueueIndex}><label>{text.selectMount}<select value={selectedMountId} onChange={(event) => setSelectedMountId(event.target.value)} required>{indexableMounts.map((mount) => <option key={mount.id} value={mount.id}>{mountLabel(mount)}</option>)}</select></label><button className="member-primary" type="submit" disabled={loading || !selectedMountId}>{text.queueIndex}</button></form>}
        {loading ? <div className="member-loading">{text.loading}</div> : jobs.length === 0 ? <div className="member-empty">{text.noIndexJobs}</div> : <table className="member-admin-table"><thead><tr><th>{text.job}</th><th>{text.status}</th><th>{text.attempts}</th><th>{text.updated}</th><th>{text.actions}</th></tr></thead><tbody>{jobs.map((job) => {
          const target = jobTargetLabel(job);
          return <tr key={job.id}><td>{jobKindLabel(job.kind ?? '--', text)}{target && <small>{target}</small>}</td><td>{statusLabel(job.status ?? '--', text)}</td><td>{job.attempts ?? 0}/{job.maxAttempts ?? 0}</td><td>{formatDate(job.updatedAt ?? job.createdAt ?? '', locale)}</td><td>{job.status === 'queued' && job.id ? <button className="member-table-action" type="button" onClick={() => void onRunJob(job.id!)} disabled={loading}>{text.run}</button> : '--'}</td></tr>;
        })}</tbody></table>}
      </>}

      {tab === 'route-groups' && <>
        <p className="member-admin-hint">{text.routeGroupsHint}</p>
        {loading && routeGroups.length === 0 ? <div className="member-loading">{text.loading}</div> : routeGroups.length === 0 ? <div className="member-empty">{text.noRouteGroups}</div> : (
          <table className="member-admin-table member-route-table">
            <thead>
              <tr>
                <th>{text.routeGroup}</th>
                <th>{text.status}</th>
                <th>{text.routeEntry}</th>
                <th>{text.routeRisk}</th>
                <th>{text.actions}</th>
              </tr>
            </thead>
            <tbody>
              {routeGroups.map((group) => (
                <tr key={group.id}>
                  <td>
                    <strong>{routeGroupLabel(group.id, text)}</strong>
                    <small>{routeGroupDetail(group.id, text)}</small>
                  </td>
                  <td>
                    <span className={`member-route-badge ${group.exposed ? 'exposed' : 'closed'}`}>
                      {group.exposed ? text.routeExposed : text.routeClosed}
                    </span>
                  </td>
                  <td>{group.entry}</td>
                  <td>{group.risk}</td>
                  <td>
                    <button
                      className={`member-table-action ${group.exposed ? 'member-table-danger' : ''}`}
                      type="button"
                      disabled={loading || pendingGroupId === group.id}
                      onClick={() => void onToggleRouteGroup(group)}
                    >
                      {group.exposed ? text.routeDisable : text.routeEnable}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </>}

      {tab === 'audit' && (loading ? <div className="member-loading">{text.loading}</div> : events.length === 0 ? <div className="member-empty">{text.noAuditEvents}</div> : (
        <table className="member-admin-table">
          <thead>
            <tr><th>{text.event}</th><th>{text.actor}</th><th>{text.target}</th><th>{text.time}</th></tr>
          </thead>
          <tbody>
            {events.map((event, index) => {
              const actor = auditActorLabel(event);
              return (
                <tr key={`${event.occurredAt}-${event.action}-${index}`}>
                  <td>{event.action}</td>
                  <td>{actor.primary}{actor.secondary && <small>{actor.secondary}</small>}</td>
                  <td>{auditTargetLabel(event)}</td>
                  <td>{formatDate(event.occurredAt, locale)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ))}
    </div>
  );
}
