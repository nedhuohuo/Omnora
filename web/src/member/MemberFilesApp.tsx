import { type FormEvent, useEffect, useState } from 'react';
import { ApiError, confirmTOTP, getBootstrap, getPreferences, getSession, initialize, login, logout, setupTOTP } from '../api';
import AdminWorkspace, { type AdminTab } from './AdminWorkspace';
import MemberAccountPanel from './MemberAccountPanel';
import AdminUpdatesPanel from './AdminUpdatesPanel';
import MemberContentNavigation from './MemberContentNavigation';
import MemberDocsPanel from './MemberDocsPanel';
import MemberSharesPanel from './MemberSharesPanel';
import MemberStorageWorkspace from './MemberStorageWorkspace';
import MemberTokensPanel from './MemberTokensPanel';
import { MemberDialogProvider } from './MemberDialog';
import { type MemberLocale, localeMessages } from './i18n';
import { RecentReauthProvider } from './RecentReauthProvider';
import MemberIcon, { type MemberIconName } from './MemberIcon';
import { stateForSession } from './sessionFlow';
import { syncAuthenticatedTheme } from './themeSync';
import { useLocale } from './useLocale';
import './member-files.css';

export type MemberTab = 'personal' | 'team-folders' | 'collaborations' | 'shares' | 'tokens' | 'docs' | 'account' | 'updates';
type AdminNavGroup = 'overview' | 'identity' | 'storage' | 'security' | 'backups' | 'settings';
type AdminNavItem = { id: AdminTab; label: string };
type AdminNavGroupItem = { id: AdminNavGroup; label: string; icon: MemberIconName; tabs: AdminNavItem[] };
type SessionState = 'checking' | 'signed-out' | 'enrollment' | 'ready';

type Props = { entry?: 'member' | 'admin' };

export function adminTopbarTitle(entry: Props['entry'], activeTab: MemberTab | AdminTab, adminItems: readonly AdminNavGroupItem[], signInLabel: string) {
  if (entry !== 'admin') return signInLabel;
  if (adminItems.length === 0) return signInLabel;
  const activeAdminGroupId = isAdminTab(activeTab) ? adminGroupForTab(activeTab) : 'overview';
  return adminItems.find((group) => group.id === activeAdminGroupId)?.label ?? adminItems[0].label;
}

export function memberTopbarTitle(activeTab: MemberTab | AdminTab, text: typeof localeMessages[MemberLocale]) {
  if (activeTab === 'personal') return text.personalSpace;
  if (activeTab === 'team-folders') return text.teamFolders;
  if (activeTab === 'collaborations') return text.collaboration;
  if (activeTab === 'shares') return text.navShares;
  if (activeTab === 'tokens') return text.navTokens;
  if (activeTab === 'docs') return text.navDocs;
  if (activeTab === 'account') return text.account;
  if (activeTab === 'updates') return text.adminUpdates;
  return text.personalSpace;
}

const defaultAdminGroupTabs: Record<AdminNavGroup, AdminTab> = {
  overview: 'overview',
  identity: 'users',
  storage: 'mounts',
  security: 'route-groups',
  backups: 'backups',
  settings: 'updates',
};

export function shouldUseAdminShell(entry: Props['entry'], isAdmin: boolean) {
  return entry === 'admin' && isAdmin;
}

export function isAdminTab(tab: MemberTab | AdminTab): tab is AdminTab {
  return ['overview', 'users', 'mounts', 'index-jobs', 'route-groups', 'share-governance', 'token-governance', 'audit', 'backups', 'updates'].includes(tab);
}

export function buildAdminNavItems(text: typeof localeMessages[MemberLocale]): AdminNavGroupItem[] {
  return [
    { id: 'overview', label: text.adminOverview, icon: 'admin', tabs: [{ id: 'overview', label: text.adminOverview }] },
    { id: 'identity', label: text.adminIdentitySection, icon: 'users', tabs: [{ id: 'users', label: text.adminUsers }] },
    { id: 'storage', label: text.adminStorageSearch, icon: 'mount', tabs: [{ id: 'mounts', label: text.adminMounts }, { id: 'index-jobs', label: text.adminIndexJobs }] },
    { id: 'security', label: text.adminAccessSecurity, icon: 'security', tabs: [{ id: 'route-groups', label: text.adminRouteGroups }, { id: 'share-governance', label: text.adminShareGovernance }, { id: 'token-governance', label: text.adminTokenGovernance }, { id: 'audit', label: text.adminAudit }] },
    { id: 'backups', label: text.adminBackups, icon: 'backup', tabs: [{ id: 'backups', label: text.adminBackups }] },
    { id: 'settings', label: text.settings, icon: 'settings', tabs: [{ id: 'updates', label: text.adminUpdates }] },
  ];
}

export function MemberSidebarNavigation({ locale, activeTab, onSelect, isAdmin }: { locale: MemberLocale; activeTab: MemberTab | AdminTab; onSelect: (tab: MemberTab | AdminTab) => void; isAdmin: boolean }) {
  const text = localeMessages[locale];
  const memberActive = activeTab as MemberTab | AdminTab;
  return <>
    <div className="member-sidebar-section">
      <p>{text.files}</p>
      <nav aria-label={text.files}>
        <MemberContentNavigation locale={locale} active={memberActive === 'personal' || memberActive === 'team-folders' || memberActive === 'collaborations' ? memberActive as any : null} onSelect={onSelect as any} />
      </nav>
    </div>
    <div className="member-sidebar-section">
      <p>{text.shared}</p>
      <nav aria-label={text.shared}>
        <button className={`member-nav ${memberActive === 'shares' ? 'active' : ''}`} type="button" onClick={() => onSelect('shares')}><MemberIcon name="share" /> <span>{text.navShares}</span></button>
        <button className={`member-nav ${memberActive === 'tokens' ? 'active' : ''}`} type="button" onClick={() => onSelect('tokens')}><MemberIcon name="token" /> <span>{text.navTokens}</span></button>
        <button className={`member-nav ${memberActive === 'docs' ? 'active' : ''}`} type="button" onClick={() => onSelect('docs')}><MemberIcon name="docs" /> <span>{text.navDocs}</span></button>
      </nav>
    </div>
    <div className="member-sidebar-section">
      <p>{text.settings}</p>
      <nav aria-label={text.settings}>
        <button className={`member-nav ${memberActive === 'account' ? 'active' : ''}`} type="button" onClick={() => onSelect('account')}><MemberIcon name="account" /> <span>{text.account}</span></button>
        {isAdmin && <button className={`member-nav ${memberActive === 'updates' ? 'active' : ''}`} type="button" onClick={() => onSelect('updates')}><MemberIcon name="settings" /> <span>{text.adminUpdates}</span></button>}
      </nav>
    </div>
  </>;
}

export function AdminSidebarNavigation({ items, activeGroupId, groupTabs, onSelect }: { items: readonly AdminNavGroupItem[]; activeGroupId: AdminNavGroup; groupTabs: Record<AdminNavGroup, AdminTab>; onSelect: (tab: AdminTab) => void }) {
  return <nav className="member-admin-nav" aria-label="Admin">
    {items.map((group) => <button className={`member-nav ${activeGroupId === group.id ? 'active' : ''}`} type="button" onClick={() => onSelect(groupTabs[group.id])} key={group.id}><MemberIcon name={group.icon} /> <span>{group.label}</span></button>)}
  </nav>;
}

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    return body?.error?.message || body?.error?.code || `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : 'Unknown error';
}

function adminGroupForTab(tab: AdminTab): AdminNavGroup {
  if (tab === 'users') return 'identity';
  if (tab === 'mounts' || tab === 'index-jobs') return 'storage';
  if (tab === 'route-groups' || tab === 'share-governance' || tab === 'token-governance' || tab === 'audit') return 'security';
  if (tab === 'backups') return 'backups';
  if (tab === 'updates') return 'settings';
  return 'overview';
}

export default function MemberFilesApp({ entry = 'member' }: Props) {
  const { locale, setLocale } = useLocale();
  const text = localeMessages[locale];
  const [sessionState, setSessionState] = useState<SessionState>('checking');
  const [isAdmin, setIsAdmin] = useState(false);
  const [isInitialAdmin, setIsInitialAdmin] = useState(false);
  const [activeTab, setActiveTab] = useState<MemberTab | AdminTab>(entry === 'admin' ? 'overview' : 'personal');
  const [adminGroupTabs, setAdminGroupTabs] = useState(defaultAdminGroupTabs);
  const [loginForm, setLoginForm] = useState({ login: '', password: '', totpCode: '' });
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [enrollment, setEnrollment] = useState<{ secret: string; otpauthUri?: string } | null>(null);
  const [enrollmentCode, setEnrollmentCode] = useState('');
  const [setupMode, setSetupMode] = useState(false);
  const [initializationAvailable, setInitializationAvailable] = useState(false);
  const [setupForm, setSetupForm] = useState({ token: '', email: '', displayName: '', password: '' });
  const [setupNotice, setSetupNotice] = useState('');

  useEffect(() => {
    void (async () => {
      try {
        const session = await getSession();
        setIsAdmin(session.isAdmin === true);
        setIsInitialAdmin(session.isInitialAdmin === true);
        const next = stateForSession(session);
        setSessionState(next);
        if (next === 'ready') await syncAuthenticatedTheme(getPreferences);
      } catch {
        setSessionState('signed-out');
        setIsAdmin(false);
        setIsInitialAdmin(false);
        try {
          const bootstrap = await getBootstrap();
          setInitializationAvailable(bootstrap.initializationAvailable === true);
        } catch {
          setInitializationAvailable(false);
        }
      }
    })();
  }, []);

  async function onLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      const session = await login({ login: loginForm.login, password: loginForm.password, totpCode: loginForm.totpCode || undefined });
      setIsAdmin(session.isAdmin === true);
      setIsInitialAdmin(session.isInitialAdmin === true);
      setSessionState(stateForSession(session));
      if (stateForSession(session) === 'ready') await syncAuthenticatedTheme(getPreferences);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function beginEnrollment() {
    setLoading(true);
    setError('');
    try {
      const response = await setupTOTP();
      setEnrollment({ secret: response.secret ?? '', otpauthUri: response.otpauthUri });
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onConfirmEnrollment(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      await confirmTOTP(enrollmentCode.trim());
      const session = await getSession();
      setIsAdmin(session.isAdmin === true);
      setIsInitialAdmin(session.isInitialAdmin === true);
      setEnrollment(null);
      setEnrollmentCode('');
      setSessionState(stateForSession(session));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onInitialize(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    setSetupNotice('');
    try {
      await initialize(setupForm);
      setSetupNotice(text.setupComplete);
      setSetupMode(false);
      setInitializationAvailable(false);
      setLoginForm({ login: setupForm.email, password: '', totpCode: '' });
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onLogout() {
    await logout().catch(() => undefined);
    setSessionState('signed-out');
    setIsAdmin(false);
    setIsInitialAdmin(false);
    setEnrollment(null);
  }

  if (sessionState === 'checking') return <main className="member-app"><div className="member-loading">{text.loading}</div></main>;
  if (sessionState === 'signed-out') return <main className="member-app member-auth-state">
    <div className="member-login-panel"><h1>{entry === 'admin' ? text.adminAccessSecurity : text.signIn}</h1>
      {initializationAvailable && <button type="button" onClick={() => setSetupMode((value) => !value)}>{text.setupTitle}</button>}
      {setupMode ? <form onSubmit={onInitialize}><label>{text.setupToken}<input value={setupForm.token} onChange={(event) => setSetupForm({ ...setupForm, token: event.target.value })} required /></label><label>{text.email}<input type="email" value={setupForm.email} onChange={(event) => setSetupForm({ ...setupForm, email: event.target.value })} required /></label><label>{text.setupDisplayName}<input value={setupForm.displayName} onChange={(event) => setSetupForm({ ...setupForm, displayName: event.target.value })} required /></label><label>{text.password}<input type="password" value={setupForm.password} onChange={(event) => setSetupForm({ ...setupForm, password: event.target.value })} required /></label><button className="member-primary" type="submit" disabled={loading}>{text.setupSubmit}</button></form> : <form onSubmit={onLogin}><label>{text.email}<input value={loginForm.login} onChange={(event) => setLoginForm({ ...loginForm, login: event.target.value })} autoComplete="username" required /></label><label>{text.password}<input type="password" value={loginForm.password} onChange={(event) => setLoginForm({ ...loginForm, password: event.target.value })} autoComplete="current-password" required /></label><label>{text.loginTotpCode}<input value={loginForm.totpCode} onChange={(event) => setLoginForm({ ...loginForm, totpCode: event.target.value })} inputMode="numeric" /></label><button className="member-primary" type="submit" disabled={loading}>{text.signInAction}</button></form>}
      {setupNotice && <p className="member-admin-notice">{setupNotice}</p>}{error && <p className="member-error">{text.error}: {error}</p>}
    </div>
  </main>;
  if (sessionState === 'enrollment' && !enrollment) return <main className="member-app member-auth-state"><div className="member-login-panel"><h1>{text.verificationCode}</h1><button className="member-primary" type="button" onClick={() => void beginEnrollment()} disabled={loading}>{text.setupSubmit}</button>{error && <p className="member-error">{text.error}: {error}</p>}</div></main>;
  if (sessionState === 'enrollment' && enrollment) return <main className="member-app member-auth-state"><div className="member-login-panel"><h1>{text.verificationCode}</h1><p>{text.verificationCode}</p>{enrollment.otpauthUri && <code>{enrollment.otpauthUri}</code>}<p>{enrollment.secret}</p><form onSubmit={onConfirmEnrollment}><label>{text.verificationCode}<input value={enrollmentCode} onChange={(event) => setEnrollmentCode(event.target.value)} required /></label><button className="member-primary" type="submit" disabled={loading}>{text.signInAction}</button></form>{error && <p className="member-error">{text.error}: {error}</p>}</div></main>;

  const adminItems = buildAdminNavItems(text);
  const activeAdminGroupId = isAdminTab(activeTab) ? adminGroupForTab(activeTab) : 'overview';
  const activeAdminGroup = adminItems.find((group) => group.id === activeAdminGroupId) ?? adminItems[0];
  const adminView = shouldUseAdminShell(entry, isAdmin);

  if (entry === 'admin' && !isAdmin) return <MemberDialogProvider>
    <RecentReauthProvider locale={locale}>
      <main className="member-app">
        <header className="member-topbar"><strong>{text.adminAccessSecurity}</strong><div className="member-top-actions"><button type="button" onClick={() => setLocale(locale === 'zh-CN' ? 'en-US' : 'zh-CN')}>{locale === 'zh-CN' ? 'EN' : '中文'}</button><button type="button" onClick={() => void onLogout()}>{text.signOut}</button></div></header>
        <section className="member-no-access"><h1>{text.adminAccessDenied}</h1><p>{text.adminAccessDetail}</p><button className="member-primary" type="button" onClick={() => { window.location.href = '/app'; }}>{text.goToFiles}</button></section>
      </main>
    </RecentReauthProvider>
  </MemberDialogProvider>;

  return <MemberDialogProvider>
    <RecentReauthProvider locale={locale}>
      <main className="member-app"><header className="member-topbar"><strong>{entry === 'admin' ? adminTopbarTitle(entry, activeTab, adminItems, text.signIn) : memberTopbarTitle(activeTab, text)}</strong><div className="member-top-actions"><button type="button" onClick={() => setLocale(locale === 'zh-CN' ? 'en-US' : 'zh-CN')}>{locale === 'zh-CN' ? 'EN' : '中文'}</button><button type="button" onClick={() => void onLogout()}>{text.signOut}</button></div></header><div className="member-layout"><aside className="member-sidebar">
      {entry === 'member' ? <MemberSidebarNavigation locale={locale} activeTab={activeTab} onSelect={setActiveTab as (tab: MemberTab | AdminTab) => void} isAdmin={isAdmin} /> : <AdminSidebarNavigation items={adminItems} activeGroupId={activeAdminGroupId} groupTabs={adminGroupTabs} onSelect={setActiveTab} />}
    </aside><section className="member-content">{adminView ? <>
      {activeAdminGroup.tabs.length > 1 && <div className="member-admin-subnav" role="tablist" aria-label={activeAdminGroup.label}>{activeAdminGroup.tabs.map((item) => <button type="button" role="tab" aria-selected={activeTab === item.id} aria-pressed={activeTab === item.id} onClick={() => { setAdminGroupTabs((current) => ({ ...current, [activeAdminGroup.id]: item.id })); setActiveTab(item.id); }} key={item.id}>{item.label}</button>)}</div>}
      <AdminWorkspace tab={activeTab as AdminTab} locale={locale} isInitialAdmin={isInitialAdmin} />
    </> : activeTab === 'personal' ? <MemberStorageWorkspace locale={locale} view="personal" /> : activeTab === 'team-folders' ? <MemberStorageWorkspace locale={locale} view="team-folders" /> : activeTab === 'collaborations' ? <MemberStorageWorkspace locale={locale} view="collaborations" /> : activeTab === 'shares' ? <MemberSharesPanel locale={locale} /> : activeTab === 'tokens' ? <MemberTokensPanel locale={locale} /> : activeTab === 'docs' ? <MemberDocsPanel locale={locale} /> : activeTab === 'updates' ? (isAdmin ? <AdminUpdatesPanel locale={locale} /> : <MemberAccountPanel locale={locale} />) : <MemberAccountPanel locale={locale} />}</section></div></main>
    </RecentReauthProvider>
  </MemberDialogProvider>;
}
