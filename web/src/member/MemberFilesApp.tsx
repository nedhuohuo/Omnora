import { type FormEvent, useEffect, useState } from 'react';
import { ApiError, confirmTOTP, getBootstrap, getPreferences, getSession, initialize, login, logout, setupTOTP } from '../api';
import AdminWorkspace, { type AdminTab } from './AdminWorkspace';
import MemberAccountPanel from './MemberAccountPanel';
import MemberContentNavigation from './MemberContentNavigation';
import MemberDocsPanel from './MemberDocsPanel';
import MemberSharesPanel from './MemberSharesPanel';
import MemberStorageWorkspace from './MemberStorageWorkspace';
import MemberTokensPanel from './MemberTokensPanel';
import { localeMessages } from './i18n';
import { RecentReauthProvider } from './RecentReauthProvider';
import { stateForSession } from './sessionFlow';
import { syncAuthenticatedTheme } from './themeSync';
import { useLocale } from './useLocale';
import './member-files.css';

type MemberTab = 'personal' | 'team-folders' | 'collaborations' | 'shares' | 'tokens' | 'docs' | 'account';
type AdminNavGroup = 'overview' | 'identity' | 'storage' | 'security' | 'backups';
type AdminNavItem = { id: AdminTab; label: string };
type AdminNavGroupItem = { id: AdminNavGroup; label: string; tabs: AdminNavItem[] };
type SessionState = 'checking' | 'signed-out' | 'enrollment' | 'ready';

type Props = { entry?: 'member' | 'admin' };

export function adminTopbarTitle(entry: Props['entry'], activeTab: MemberTab | AdminTab, adminItems: readonly AdminNavGroupItem[], signInLabel: string) {
  if (entry !== 'admin') return signInLabel;
  if (adminItems.length === 0) return signInLabel;
  const activeAdminGroupId = typeof activeTab === 'string' && ['overview', 'users', 'mounts', 'index-jobs', 'route-groups', 'share-governance', 'token-governance', 'audit', 'backups'].includes(activeTab) ? adminGroupForTab(activeTab as AdminTab) : 'overview';
  return adminItems.find((group) => group.id === activeAdminGroupId)?.label ?? adminItems[0].label;
}

const defaultAdminGroupTabs: Record<AdminNavGroup, AdminTab> = {
  overview: 'overview',
  identity: 'users',
  storage: 'mounts',
  security: 'route-groups',
  backups: 'backups',
};

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

  if (sessionState === 'checking') return <main className="member-app-shell"><div className="member-loading">{text.loading}</div></main>;
  if (sessionState === 'signed-out') return <main className="member-app-shell member-auth-shell">
    <div className="member-auth-card"><h1>{entry === 'admin' ? text.adminAccessSecurity : text.signIn}</h1>
      {initializationAvailable && <button type="button" onClick={() => setSetupMode((value) => !value)}>{text.setupTitle}</button>}
      {setupMode ? <form onSubmit={onInitialize}><label>{text.setupToken}<input value={setupForm.token} onChange={(event) => setSetupForm({ ...setupForm, token: event.target.value })} required /></label><label>{text.email}<input type="email" value={setupForm.email} onChange={(event) => setSetupForm({ ...setupForm, email: event.target.value })} required /></label><label>{text.setupDisplayName}<input value={setupForm.displayName} onChange={(event) => setSetupForm({ ...setupForm, displayName: event.target.value })} required /></label><label>{text.password}<input type="password" value={setupForm.password} onChange={(event) => setSetupForm({ ...setupForm, password: event.target.value })} required /></label><button className="member-primary" type="submit" disabled={loading}>{text.setupSubmit}</button></form> : <form onSubmit={onLogin}><label>{text.email}<input value={loginForm.login} onChange={(event) => setLoginForm({ ...loginForm, login: event.target.value })} autoComplete="username" required /></label><label>{text.password}<input type="password" value={loginForm.password} onChange={(event) => setLoginForm({ ...loginForm, password: event.target.value })} autoComplete="current-password" required /></label><label>{text.verificationCode}<input value={loginForm.totpCode} onChange={(event) => setLoginForm({ ...loginForm, totpCode: event.target.value })} inputMode="numeric" /></label><button className="member-primary" type="submit" disabled={loading}>{text.signInAction}</button></form>}
      {setupNotice && <p className="member-admin-notice">{setupNotice}</p>}{error && <p className="member-error">{text.error}: {error}</p>}
    </div>
  </main>;
  if (sessionState === 'enrollment' && !enrollment) return <main className="member-app-shell member-auth-shell"><div className="member-auth-card"><h1>{text.verificationCode}</h1><button className="member-primary" type="button" onClick={() => void beginEnrollment()} disabled={loading}>{text.setupSubmit}</button>{error && <p className="member-error">{text.error}: {error}</p>}</div></main>;
  if (sessionState === 'enrollment' && enrollment) return <main className="member-app-shell member-auth-shell"><div className="member-auth-card"><h1>{text.verificationCode}</h1><p>{text.verificationCode}</p>{enrollment.otpauthUri && <code>{enrollment.otpauthUri}</code>}<p>{enrollment.secret}</p><form onSubmit={onConfirmEnrollment}><label>{text.verificationCode}<input value={enrollmentCode} onChange={(event) => setEnrollmentCode(event.target.value)} required /></label><button className="member-primary" type="submit" disabled={loading}>{text.signInAction}</button></form>{error && <p className="member-error">{text.error}: {error}</p>}</div></main>;

  const adminItems: AdminNavGroupItem[] = [
    { id: 'overview', label: text.adminOverview, tabs: [{ id: 'overview', label: text.adminOverview }] },
    { id: 'identity', label: text.adminIdentitySection, tabs: [{ id: 'users', label: text.adminUsers }] },
    { id: 'storage', label: text.adminStorageSearch, tabs: [{ id: 'mounts', label: text.adminMounts }, { id: 'index-jobs', label: text.adminIndexJobs }] },
    { id: 'security', label: text.adminAccessSecurity, tabs: [{ id: 'route-groups', label: text.adminRouteGroups }, { id: 'share-governance', label: text.adminShareGovernance }, { id: 'token-governance', label: text.adminTokenGovernance }, { id: 'audit', label: text.adminAudit }] },
    { id: 'backups', label: text.adminBackups, tabs: [{ id: 'backups', label: text.adminBackups }] },
  ];
  const activeAdminGroupId = typeof activeTab === 'string' && ['overview', 'users', 'mounts', 'index-jobs', 'route-groups', 'share-governance', 'token-governance', 'audit', 'backups'].includes(activeTab) ? adminGroupForTab(activeTab as AdminTab) : 'overview';
  const activeAdminGroup = adminItems.find((group) => group.id === activeAdminGroupId) ?? adminItems[0];
  const adminView = isAdmin && (entry === 'admin' || activeTab === 'overview' || activeTab === 'users' || activeTab === 'mounts' || activeTab === 'index-jobs' || activeTab === 'route-groups' || activeTab === 'share-governance' || activeTab === 'token-governance' || activeTab === 'backups' || activeTab === 'audit');

  return <RecentReauthProvider locale={locale}>
    <main className="member-app-shell"><header className="member-topbar"><strong>{adminTopbarTitle(entry, activeTab, adminItems, text.signIn)}</strong><div className="member-topbar-actions"><button type="button" onClick={() => setLocale(locale === 'zh-CN' ? 'en-US' : 'zh-CN')}>{locale === 'zh-CN' ? 'EN' : '中文'}</button><button type="button" onClick={() => void onLogout()}>{text.signOut}</button></div></header><div className="member-layout"><aside className="member-sidebar">
      {entry === 'member' && <><MemberContentNavigation locale={locale} active={activeTab === 'personal' || activeTab === 'team-folders' || activeTab === 'collaborations' ? activeTab : null} onSelect={setActiveTab} /><button className={`member-nav ${activeTab === 'shares' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('shares')}>{text.navShares}</button><button className={`member-nav ${activeTab === 'tokens' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('tokens')}>{text.navTokens}</button><button className={`member-nav ${activeTab === 'docs' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('docs')}>{text.navDocs}</button><button className={`member-nav ${activeTab === 'account' ? 'active' : ''}`} type="button" onClick={() => setActiveTab('account')}>{text.account}</button></>}
      {isAdmin && <>{adminItems.map((group) => <button className={`member-nav ${activeAdminGroupId === group.id ? 'active' : ''}`} type="button" onClick={() => setActiveTab(adminGroupTabs[group.id])} key={group.id}>{group.label}</button>)}</>}
    </aside><section className="member-main-content">{adminView ? <>
      {activeAdminGroup.tabs.length > 1 && <div className="member-admin-subnav" role="tablist" aria-label={activeAdminGroup.label}>{activeAdminGroup.tabs.map((item) => <button type="button" role="tab" aria-selected={activeTab === item.id} aria-pressed={activeTab === item.id} onClick={() => { setAdminGroupTabs((current) => ({ ...current, [activeAdminGroup.id]: item.id })); setActiveTab(item.id); }} key={item.id}>{item.label}</button>)}</div>}
      <AdminWorkspace tab={activeTab as AdminTab} locale={locale} isInitialAdmin={isInitialAdmin} />
    </> : activeTab === 'personal' ? <MemberStorageWorkspace locale={locale} view="personal" /> : activeTab === 'team-folders' ? <MemberStorageWorkspace locale={locale} view="team-folders" /> : activeTab === 'collaborations' ? <MemberStorageWorkspace locale={locale} view="collaborations" /> : activeTab === 'shares' ? <MemberSharesPanel locale={locale} /> : activeTab === 'tokens' ? <MemberTokensPanel locale={locale} /> : activeTab === 'docs' ? <MemberDocsPanel locale={locale} /> : <MemberAccountPanel locale={locale} />}</section></div></main>
  </RecentReauthProvider>;
}
