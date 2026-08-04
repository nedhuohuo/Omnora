import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import {
  type AdminMountListItem,
  type AdminRouteGroupItem,
  type AdminSpacePayload,
  type AuditEventPayload,
  type HostDirectoryEntry,
  type JobPayload,
  ApiError,
  deleteAdminMount,
  enqueueIndexJob,
  listAdminHostDirectories,
  listAdminMounts,
  listAdminRouteGroups,
  listAdminSpaces,
  listAuditEvents,
  listIndexJobs,
  registerAdminMount,
  renameAdminMount,
  reverifyAdminMount,
  runIndexJob,
  updateAdminRouteGroup,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { readableLabel } from './displayLabels';
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
  | 'mounts'
  | 'index-jobs'
  | 'route-groups'
  | 'share-governance'
  | 'token-governance'
  | 'backups'
  | 'audit';

type MountForm = {
  spaceId: string;
  displayName: string;
  rootPath: string;
  kind: 'external' | 'managed';
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
};

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

function defaultRootForKind(kind: MountForm['kind'], roots: string[]) {
  if (kind === 'managed') {
    return roots.find((root) => root.includes('/managed')) ?? roots[0] ?? '';
  }
  return roots.find((root) => root.includes('/mnt/')) ?? roots[0] ?? '';
}

export default function AdminWorkspace({ tab, locale }: { tab: AdminTab; locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [spaces, setSpaces] = useState<AdminSpacePayload[]>([]);
  const [mounts, setMounts] = useState<AdminMountListItem[]>([]);
  const [jobs, setJobs] = useState<JobPayload[]>([]);
  const [routeGroups, setRouteGroups] = useState<AdminRouteGroupItem[]>([]);
  const [events, setEvents] = useState<AuditEventPayload[]>([]);
  const [mountForm, setMountForm] = useState<MountForm>({ spaceId: '', displayName: '', rootPath: '', kind: 'external', mode: 'read_write', indexEnabled: true });
  const [selectedMountId, setSelectedMountId] = useState('');
  const [loading, setLoading] = useState(false);
  const [pendingGroupId, setPendingGroupId] = useState('');
  const [error, setError] = useState('');
  const [operationComplete, setOperationComplete] = useState(false);
  const [allowedRoots, setAllowedRoots] = useState<string[]>([]);
  const [pathSuggestions, setPathSuggestions] = useState<HostDirectoryEntry[]>([]);
  const [showPathSuggestions, setShowPathSuggestions] = useState(false);
  const [renameTarget, setRenameTarget] = useState<AdminMountListItem | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<AdminMountListItem | null>(null);
  const [deleteData, setDeleteData] = useState(false);
  const suggestionRequest = useRef(0);

  const loadMountData = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [spaceResponse, mountResponse, hostDirs] = await Promise.all([
        listAdminSpaces(),
        listAdminMounts(),
        listAdminHostDirectories('/'),
      ]);
      setSpaces(spaceResponse.items);
      setMounts(mountResponse.items);
      const roots = hostDirs.roots ?? [];
      setAllowedRoots(roots);
      setMountForm((current) => ({
        ...current,
        spaceId: spaceResponse.items.some((space) => space.id === current.spaceId) ? current.spaceId : (spaceResponse.items[0]?.id ?? ''),
        rootPath: current.rootPath || defaultRootForKind(current.kind, roots),
      }));
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
    if (tab === 'mounts') void loadMountData();
    if (tab === 'index-jobs') void loadJobs();
    if (tab === 'route-groups') void loadRouteGroups();
    if (tab === 'audit') void loadAudit();
  }, [loadAudit, loadJobs, loadMountData, loadRouteGroups, tab]);

  useEffect(() => {
    if (tab !== 'mounts') return;
    const requestId = ++suggestionRequest.current;
    const timer = window.setTimeout(() => {
      void listAdminHostDirectories(mountForm.rootPath || '/')
        .then((response) => {
          if (requestId !== suggestionRequest.current) return;
          setAllowedRoots(response.roots ?? []);
          setPathSuggestions(response.entries ?? []);
        })
        .catch(() => {
          if (requestId !== suggestionRequest.current) return;
          setPathSuggestions([]);
        });
    }, 180);
    return () => window.clearTimeout(timer);
  }, [mountForm.rootPath, tab]);

  async function onCreateMount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await registerAdminMount(mountForm);
      setMountForm((current) => ({ ...current, displayName: '', rootPath: defaultRootForKind(current.kind, allowedRoots) }));
      setOperationComplete(true);
      await loadMountData();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  function openRename(mount: AdminMountListItem) {
    setError('');
    setOperationComplete(false);
    setRenameTarget(mount);
    setRenameValue(mount.name);
  }

  function openDelete(mount: AdminMountListItem) {
    setError('');
    setOperationComplete(false);
    setDeleteTarget(mount);
    setDeleteData(false);
  }

  async function onRenameMount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!renameTarget || !renameValue.trim()) return;
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await renameAdminMount(renameTarget.id, renameValue.trim());
      setRenameTarget(null);
      setRenameValue('');
      setOperationComplete(true);
      await loadMountData();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onDeleteMount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!deleteTarget) return;
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await deleteAdminMount(deleteTarget.id, deleteData);
      setDeleteTarget(null);
      setDeleteData(false);
      setOperationComplete(true);
      await loadMountData();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
      await loadMountData();
    }
  }

  async function onReverifyMount(mount: AdminMountListItem) {
    setLoading(true);
    setError('');
    setOperationComplete(false);
    try {
      await reverifyAdminMount(mount.id);
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

  const refresh = tab === 'mounts' ? loadMountData : tab === 'index-jobs' ? loadJobs : tab === 'route-groups' ? loadRouteGroups : loadAudit;
  const indexableMounts = mounts.filter(isIndexableMount);
  const heading = tab === 'mounts' ? text.mountManagement : tab === 'index-jobs' ? text.indexJobs : tab === 'route-groups' ? text.routeGroups : text.auditLog;
  const headingDetail = tab === 'mounts' ? text.mountManagementDetail : tab === 'index-jobs' ? text.indexJobsDetail : tab === 'route-groups' ? text.routeGroupsDetail : text.auditLogDetail;

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

      {tab === 'mounts' && <>
        <form className="member-admin-form" onSubmit={onCreateMount}>
          <label>{text.mountSpace}<select value={mountForm.spaceId} onChange={(event) => setMountForm({ ...mountForm, spaceId: event.target.value })} required><option value="" disabled>{text.mountSpace}</option>{spaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}</select></label>
          <label>{text.mountName}<input value={mountForm.displayName} onChange={(event) => setMountForm({ ...mountForm, displayName: event.target.value })} required /></label>
          <label className="member-admin-form-wide">
            {text.mountRoot}
            <div className="member-path-suggest">
              <input
                value={mountForm.rootPath}
                onChange={(event) => setMountForm({ ...mountForm, rootPath: event.target.value })}
                onFocus={() => setShowPathSuggestions(true)}
                onBlur={() => window.setTimeout(() => setShowPathSuggestions(false), 120)}
                placeholder={text.mountRootPlaceholder}
                autoComplete="off"
                required
              />
              {showPathSuggestions && (
                <div className="member-path-suggest-menu" role="listbox" aria-label={text.mountRoot}>
                  {pathSuggestions.length === 0 ? (
                    <div className="member-path-suggest-empty">{text.noPathSuggestions}</div>
                  ) : pathSuggestions.map((entry) => (
                    <button
                      key={entry.path}
                      type="button"
                      className="member-path-suggest-item"
                      onMouseDown={(event) => event.preventDefault()}
                      onClick={() => {
                        setMountForm({ ...mountForm, rootPath: entry.path });
                        setShowPathSuggestions(true);
                      }}
                    >
                      <span>{entry.path}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
            <small className="member-path-hint">{text.mountRootHint}</small>
            {allowedRoots.length > 0 && (
              <div className="member-path-roots">
                <span>{text.allowedRoots}</span>
                {allowedRoots.map((root) => (
                  <button
                    key={root}
                    type="button"
                    className="member-path-root"
                    onClick={() => setMountForm({ ...mountForm, rootPath: root })}
                  >
                    {root}
                  </button>
                ))}
              </div>
            )}
          </label>
          <label>{text.mountKind}<select value={mountForm.kind} onChange={(event) => {
            const kind = event.target.value as MountForm['kind'];
            setMountForm({ ...mountForm, kind, rootPath: defaultRootForKind(kind, allowedRoots) });
          }}><option value="external">{text.externalMount}</option><option value="managed">{text.managedMount}</option></select></label>
          <label>{text.mountMode}<select value={mountForm.mode} onChange={(event) => setMountForm({ ...mountForm, mode: event.target.value as MountForm['mode'] })}><option value="read_write">{text.readWrite}</option><option value="read_only">{text.readOnly}</option></select></label>
          <label className="member-admin-checkbox"><input type="checkbox" checked={mountForm.indexEnabled} onChange={(event) => setMountForm({ ...mountForm, indexEnabled: event.target.checked })} />{text.enableIndex}</label>
          <div className="member-admin-form-actions"><button className="member-primary" type="submit" disabled={loading || !mountForm.spaceId}>{text.createMount}</button></div>
        </form>
        {loading ? <div className="member-loading">{text.loading}</div> : mounts.length === 0 ? <div className="member-empty">{spaces.length === 0 ? text.noAdminSpaces : text.noAdminMounts}</div> : <table className="member-admin-table"><thead><tr><th>{text.mountName}</th><th>{text.mountSpace}</th><th>{text.mountMode}</th><th>{text.enableIndex}</th><th>{text.status}</th><th>{text.actions}</th></tr></thead><tbody>{mounts.map((mount) => <tr key={mount.id}><td>{mount.name}</td><td>{mount.space}</td><td>{mountModeLabel(mount.mode, text)}</td><td>{indexLabel(mount.index, text)}</td><td>{statusLabel(mount.health, text)}</td><td><div className="member-admin-table-actions">{mount.health !== 'active' && <button className="member-table-action" type="button" onClick={() => void onReverifyMount(mount)} disabled={loading}>{text.reverifyMount}</button>}<button className="member-table-action" type="button" onClick={() => openRename(mount)} disabled={loading}>{text.renameMount}</button><button className="member-table-action member-table-danger" type="button" onClick={() => openDelete(mount)} disabled={loading}>{text.deleteMount}</button></div></td></tr>)}</tbody></table>}
        {renameTarget && (
          <div className="member-modal-backdrop">
            <form className="member-modal" onSubmit={onRenameMount}>
              <h2>{text.renameMount}</h2>
              <label>{text.mountName}<input autoFocus value={renameValue} onChange={(event) => setRenameValue(event.target.value)} required /></label>
              <div>
                <button type="button" onClick={() => setRenameTarget(null)}>{text.cancel}</button>
                <button className="member-primary" type="submit" disabled={loading || !renameValue.trim()}>{text.saveMountName}</button>
              </div>
            </form>
          </div>
        )}
        {deleteTarget && (
          <div className="member-modal-backdrop">
            <form className="member-modal" onSubmit={onDeleteMount}>
              <h2>{text.deleteMount}</h2>
              <p className="member-modal-hint">{text.deleteMountDetail}</p>
              <p className="member-modal-hint"><strong>{deleteTarget.name}</strong> · {deleteTarget.space}</p>
              <label className="member-admin-checkbox">
                <input type="checkbox" checked={deleteData} onChange={(event) => setDeleteData(event.target.checked)} />
                {text.deleteMountData}
              </label>
              <p className="member-modal-hint">{text.deleteMountDataHint}</p>
              <div>
                <button type="button" onClick={() => { setDeleteTarget(null); setDeleteData(false); }}>{text.cancel}</button>
                <button className="member-modal-danger" type="submit" disabled={loading}>{text.confirmDeleteMount}</button>
              </div>
            </form>
          </div>
        )}
      </>}

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
