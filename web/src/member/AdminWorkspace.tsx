import { useCallback, useEffect, useState } from 'react';
import {
  type AdminRouteGroupItem,
  type AuditEventPayload,
  ApiError,
  isReauthenticationCanceled,
  listAdminRouteGroups,
  listAuditEvents,
  updateAdminRouteGroup,
} from '../api';
import { useMemberDialog } from './MemberDialog';
import { type MemberLocale, localeMessages } from './i18n';
import { readableLabel } from './displayLabels';
import { copyText } from './clipboard';
import { getMcpEndpoint } from './mcpIntegration';
import AdminIndexJobsPanel from './AdminIndexJobsPanel';
import AdminMountsPanel from './AdminMountsPanel';
import {
  AdminBackupsPanel,
  AdminOverviewPanel,
  AdminShareGovernancePanel,
  AdminTokenGovernancePanel,
  AdminUsersPanel,
} from './AdminPanels';

export type AdminTab =
  | 'overview'
  | 'users'
  | 'mounts'
  | 'index-jobs'
  | 'route-groups'
  | 'share-governance'
  | 'token-governance'
  | 'backups'
  | 'audit';

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

function isAdminToggleableRouteGroup(id: string) {
  return id === 'share' || id === 'rest' || id === 'mcp' || id === 'openapi';
}

function formatDate(value: string, locale: MemberLocale) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
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

function routeGroupEntry(group: AdminRouteGroupItem) {
  const origin = globalThis.location?.origin ?? '';
  if (group.id === 'mcp') return getMcpEndpoint(origin);
  if (group.id === 'openapi') return `${origin.replace(/\/+$/, '')}/openapi/omnora.v1.yaml`;
  return group.entry;
}

function auditActorLabel(event: AuditEventPayload) {
  const label = readableLabel(event.actorLabel);
  const displayName = readableLabel(event.actorDisplayName);
  const email = readableLabel(event.actorEmail);
  const actor = event.actor === 'system' ? 'system' : readableLabel(event.actor);
  return { primary: label || displayName || email || actor || '--', secondary: email && email !== label && email !== displayName ? email : '' };
}

function auditTargetLabel(event: AuditEventPayload) {
  const label = readableLabel(event.targetLabel) || readableLabel(event.targetId);
  return label ? `${event.targetType} / ${label}` : event.targetType;
}

export default function AdminWorkspace({ tab, locale, isInitialAdmin }: { tab: AdminTab; locale: MemberLocale; isInitialAdmin: boolean }) {
  const text = localeMessages[locale];
  const { confirm } = useMemberDialog();
  const [routeGroups, setRouteGroups] = useState<AdminRouteGroupItem[]>([]);
  const [events, setEvents] = useState<AuditEventPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [pendingGroupId, setPendingGroupId] = useState('');
  const [copiedRouteGroupId, setCopiedRouteGroupId] = useState('');
  const [error, setError] = useState('');
  const [operationComplete, setOperationComplete] = useState(false);

  const loadRouteGroups = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminRouteGroups();
      setRouteGroups((response.items ?? []).filter((group) => isAdminToggleableRouteGroup(group.id)));
    } catch (caught) {
      const detail = describeError(caught);
      if (detail) setError(detail);
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
    if (tab === 'route-groups') void loadRouteGroups();
    if (tab === 'audit') void loadAudit();
  }, [loadAudit, loadRouteGroups, tab]);

  async function onToggleRouteGroup(group: AdminRouteGroupItem) {
    if (!isAdminToggleableRouteGroup(group.id)) return;
    const nextExposed = !group.exposed;
    if (!nextExposed && group.id === 'rest') {
      const confirmed = await confirm({
        title: text.routeLockoutTitle,
        description: text.routeLockoutWarn,
        confirmLabel: text.routeDisable,
        cancelLabel: text.cancel,
        tone: 'danger',
      });
      if (!confirmed) return;
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
      const detail = describeError(caught);
      if (detail) setError(detail);
    } finally {
      setPendingGroupId('');
      setLoading(false);
    }
  }

  if (tab === 'overview') return <AdminOverviewPanel locale={locale} />;
  if (tab === 'users') return <AdminUsersPanel locale={locale} />;
  if (tab === 'mounts') return <AdminMountsPanel locale={locale} isInitialAdmin={isInitialAdmin} />;
  if (tab === 'index-jobs') return <AdminIndexJobsPanel locale={locale} />;
  if (tab === 'share-governance') return <AdminShareGovernancePanel locale={locale} />;
  if (tab === 'token-governance') return <AdminTokenGovernancePanel locale={locale} />;
  if (tab === 'backups') return <AdminBackupsPanel locale={locale} />;

  const refresh = tab === 'route-groups' ? loadRouteGroups : loadAudit;
  return (
    <div className="member-admin-workspace">
      <div className="member-heading"><div><h1>{tab === 'route-groups' ? text.routeGroups : text.auditLog}</h1><p>{tab === 'route-groups' ? text.routeGroupsDetail : text.auditLogDetail}</p></div><button className="member-secondary-action" type="button" onClick={() => void refresh()} disabled={loading}>{text.refresh}</button></div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {operationComplete && <p className="member-admin-notice">{text.adminOperationComplete}</p>}

      {tab === 'route-groups' && <>
        <p className="member-admin-hint">{text.routeGroupsHint}</p>
        {loading && routeGroups.length === 0 ? <div className="member-loading">{text.loading}</div> : routeGroups.length === 0 ? <div className="member-empty">{text.noRouteGroups}</div> : <table className="member-admin-table member-route-table"><thead><tr><th>{text.routeGroup}</th><th>{text.status}</th><th>{text.routeEntry}</th><th>{text.routeRisk}</th><th>{text.actions}</th></tr></thead><tbody>{routeGroups.map((group) => <tr key={group.id}><td><strong>{routeGroupLabel(group.id, text)}</strong><small>{routeGroupDetail(group.id, text)}</small></td><td><span className={`member-route-badge ${group.exposed ? 'exposed' : 'closed'}`}>{group.exposed ? text.routeExposed : text.routeClosed}</span></td><td><code className="member-route-entry">{routeGroupEntry(group)}</code>{(group.id === 'mcp' || group.id === 'openapi') && <button className="member-route-copy" type="button" onClick={() => void copyText(routeGroupEntry(group)).then((ok) => setCopiedRouteGroupId(ok ? group.id : ''))}>{text.routeCopyEntry}</button>}{copiedRouteGroupId === group.id && <small>{text.routeCopied}</small>}</td><td>{group.risk}</td><td><button className={`member-table-action ${group.exposed ? 'member-table-danger' : ''}`} type="button" disabled={loading || pendingGroupId === group.id} onClick={() => void onToggleRouteGroup(group)}>{group.exposed ? text.routeDisable : text.routeEnable}</button>{group.exposed && <small className="member-route-next-request">{text.routeDisableNextRequest}</small>}</td></tr>)}</tbody></table>}
      </>}

      {tab === 'audit' && (loading ? <div className="member-loading">{text.loading}</div> : events.length === 0 ? <div className="member-empty">{text.noAuditEvents}</div> : <table className="member-admin-table"><thead><tr><th>{text.event}</th><th>{text.actor}</th><th>{text.target}</th><th>{text.time}</th></tr></thead><tbody>{events.map((event, index) => { const actor = auditActorLabel(event); return <tr key={`${event.occurredAt}-${event.action}-${index}`}><td>{event.action}</td><td>{actor.primary}{actor.secondary && <small>{actor.secondary}</small>}</td><td>{auditTargetLabel(event)}</td><td>{formatDate(event.occurredAt, locale)}</td></tr>; })}</tbody></table>)}
    </div>
  );
}
