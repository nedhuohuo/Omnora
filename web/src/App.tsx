import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from 'react';
import {
  ApiError,
  callMCP,
  clearShareFragment,
  cancelUpload,
  completeUpload,
  confirmTOTP,
  createAiToken,
  createShare,
  createUpload,
  downloadRange,
  enqueueIndexJob,
  exchangeShareFragment,
  exchangeShareFragmentWithPassword,
  getBootstrap,
  getHealth,
  getSession,
  getUpload,
  initialize,
  listAiTokens,
  listAuditEvents,
  listIndexJobs,
  listDirectoryChildren,
  login,
  logout,
  parseShareFragment,
  registerAdminMount,
  revokeAiToken,
  runIndexJob,
  searchSpace,
  setupTOTP,
  uploadPart,
  type CreateAiTokenPayload,
  type CreateSharePayload,
  type CreateUploadPayload,
  type ShareFragment,
} from './api';
import { mockBootstrap } from './mockData';
import type { AppBootstrap, FileItem, Tone } from './types';

type Surface = 'member' | 'admin' | 'share';
type ShareState = 'valid' | 'password' | 'missing' | 'expired' | 'unavailable';
type HealthState = 'checking' | 'online' | 'offline';
type BootstrapState = 'loading' | 'api' | 'demo';
type ShareExchangeState = {
  status: 'idle' | 'missing' | 'exchanging' | 'valid' | 'password' | 'failed' | 'unavailable';
  tone: Tone;
  message: string;
  publicId?: string;
};
type OperationFeedback = {
  status: 'idle' | 'loading' | 'success' | 'mock' | 'error';
  tone: Tone;
  title: string;
  detail: string;
};
type DirectoryRow = {
  name: string;
  kind: string;
  size: string;
};

const idleFeedback: OperationFeedback = {
  status: 'idle',
  tone: 'muted',
  title: 'Ready',
  detail: 'Submit a form to call the API. Backend misses fall back to demo-safe UI state.',
};
const aiScopes = ['spaces:read', 'files:list', 'files:metadata', 'files:text', 'files:download_ticket', 'search:read', 'uploads:create'];

const surfaceCopy: Record<Surface, { eyebrow: string; title: string; description: string }> = {
  member: {
    eyebrow: 'member_web',
    title: 'Member file workspace',
    description: 'Browse spaces, confirm mount/index state, manage transfers, shares, tokens, and account security.',
  },
  admin: {
    eyebrow: 'admin_web',
    title: 'Admin control plane',
    description: 'Operate system resources without exposing member personal-space content by default.',
  },
  share: {
    eyebrow: 'share_web',
    title: 'Public share visitor',
    description: 'Anonymous share access with fragment-only secret handling, password exchange states, and scoped browsing.',
  },
};

const bootstrapKeys: Array<keyof AppBootstrap> = [
  'routeGroups',
  'mounts',
  'files',
  'transfers',
  'adminRisks',
  'auditRows',
  'shares',
  'tokens',
];

function mergeBootstrap(payload: Partial<AppBootstrap>): AppBootstrap | null {
  let hasApiData = false;
  const next: AppBootstrap = { ...mockBootstrap };

  for (const key of bootstrapKeys) {
    const value = payload[key];

    if (Array.isArray(value)) {
      next[key] = value as never;
      hasApiData = true;
    }
  }

  return hasApiData ? next : null;
}

function apiErrorDetail(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body;
    if (body && typeof body === 'object' && 'message' in body) {
      return `HTTP ${error.status}: ${String((body as { message?: unknown }).message)}`;
    }
    if (body && typeof body === 'object' && 'code' in body) {
      return `HTTP ${error.status}: ${String((body as { code?: unknown }).code)}`;
    }
    return `HTTP ${error.status}: ${error.message}`;
  }

  if (error instanceof Error) {
    return error.message;
  }

  return 'Unknown error';
}

function isMockFallbackError(error: unknown) {
  if (error instanceof ApiError) {
    return error.status === 404 || error.status === 405 || error.status === 503;
  }

  return true;
}

function summarizeJson(value: unknown) {
  if (value === undefined) {
    return 'No response body.';
  }

  try {
    return JSON.stringify(value, null, 2).slice(0, 800);
  } catch {
    return String(value);
  }
}

function operationTone(status: OperationFeedback['status']): Tone {
  if (status === 'success') {
    return 'ok';
  }
  if (status === 'mock') {
    return 'warn';
  }
  if (status === 'error') {
    return 'danger';
  }
  if (status === 'loading') {
    return 'info';
  }
  return 'muted';
}

function buildShareUrl(fragment: string) {
  return `${window.location.origin}/s#${encodeURIComponent(fragment)}`;
}

function normalizeDirectoryRows(payload: { entries?: unknown[]; items?: unknown[] }, fallback: FileItem[]): DirectoryRow[] {
  const rows = payload.entries ?? payload.items;

  if (!Array.isArray(rows) || rows.length === 0) {
    return fallback.slice(0, 4).map((file) => ({
      name: file.name,
      kind: file.kind,
      size: file.size,
    }));
  }

  return rows.slice(0, 8).map((item) => {
    if (item && typeof item === 'object') {
      const row = item as Record<string, unknown>;
      return {
        name: String(row.name ?? row.displayName ?? row.path ?? 'unnamed'),
        kind: String(row.kind ?? row.type ?? 'object'),
        size: String(row.size ?? row.sizeBytes ?? 'unknown'),
      };
    }

    return { name: String(item), kind: 'object', size: 'unknown' };
  });
}

function Badge({ tone = 'muted', children }: { tone?: Tone; children: ReactNode }) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

function Meter({ value, label }: { value: number; label: string }) {
  return (
    <div className="meter" aria-label={`${label}: ${value}%`}>
      <span style={{ width: `${value}%` }} />
    </div>
  );
}

function ShellButton({
  surface,
  current,
  onSelect,
}: {
  surface: Surface;
  current: Surface;
  onSelect: (surface: Surface) => void;
}) {
  const copy = surfaceCopy[surface];
  return (
    <button
      className="surface-tab"
      type="button"
      aria-pressed={current === surface}
      onClick={() => onSelect(surface)}
    >
      <span>{copy.eyebrow}</span>
      <strong>{surface}</strong>
    </button>
  );
}

function ConnectionPill({ health, bootstrap }: { health: HealthState; bootstrap: BootstrapState }) {
  const meta = getConnectionMeta(health, bootstrap);

  return (
    <div className="connection-pill" role="status" aria-live="polite">
      <Badge tone={meta.tone}>{meta.label}</Badge>
      <span>{meta.detail}</span>
    </div>
  );
}

function OperationStatus({ feedback, onClear }: { feedback: OperationFeedback; onClear: () => void }) {
  return (
    <div className={`operation-status operation-${feedback.status}`} role="status" aria-live="polite">
      <div>
        <Badge tone={feedback.tone}>{feedback.title}</Badge>
        <p>{feedback.detail}</p>
      </div>
      <button type="button" onClick={onClear}>Clear</button>
    </div>
  );
}

function setLoading(setFeedback: (feedback: OperationFeedback) => void, title: string, detail: string) {
  setFeedback({ status: 'loading', tone: operationTone('loading'), title, detail });
}

function getConnectionMeta(health: HealthState, bootstrap: BootstrapState) {
  if (health === 'checking' || bootstrap === 'loading') {
    return {
      tone: 'info' as Tone,
      label: 'checking backend',
      detail: 'GET /healthz + /api/v1/bootstrap',
    };
  }

  if (health === 'online' && bootstrap === 'api') {
    return {
      tone: 'ok' as Tone,
      label: 'backend connected',
      detail: 'live bootstrap data',
    };
  }

  if (health === 'online') {
    return {
      tone: 'warn' as Tone,
      label: 'demo fallback',
      detail: 'healthz ok, bootstrap unavailable',
    };
  }

  return {
    tone: 'danger' as Tone,
    label: 'demo fallback',
    detail: 'backend unavailable',
  };
}

function mapShareExchangeToState(exchange: ShareExchangeState): ShareState {
  if (exchange.status === 'valid') {
    return 'valid';
  }

  if (exchange.status === 'password') {
    return 'password';
  }

  if (exchange.status === 'failed') {
    return 'expired';
  }

  if (exchange.status === 'unavailable') {
    return 'unavailable';
  }

  return 'missing';
}

function App() {
  const [surface, setSurface] = useState<Surface>('member');
  const [appData, setAppData] = useState<AppBootstrap>(mockBootstrap);
  const [health, setHealth] = useState<HealthState>('checking');
  const [bootstrap, setBootstrap] = useState<BootstrapState>('loading');
  const [shareExchange, setShareExchange] = useState<ShareExchangeState>({
    status: 'idle',
    tone: 'muted',
    message: 'Waiting for a share fragment.',
  });
  const copy = surfaceCopy[surface];

  useEffect(() => {
    const controller = new AbortController();

    async function loadHealth() {
      try {
        await getHealth(controller.signal);
        setHealth('online');
      } catch (error) {
        if (!controller.signal.aborted) {
          setHealth('offline');
        }
      }
    }

    async function loadBootstrap() {
      try {
        const payload = await getBootstrap(controller.signal);
        const next = mergeBootstrap(payload);

        if (next) {
          setAppData(next);
          setBootstrap('api');
        } else {
          setBootstrap('demo');
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setBootstrap('demo');
        }
      }
    }

    async function exchangeInitialShareFragment() {
      const fragment = parseShareFragment();

      if (!fragment) {
        setShareExchange({
          status: 'missing',
          tone: 'warn',
          message: 'No URL fragment secret was found; no share-session request was sent.',
        });
        return;
      }

      setShareExchange({
        status: 'exchanging',
        tone: 'info',
        message: `Exchanging ${fragment.publicId} with /api/v1/share-sessions.`,
        publicId: fragment.publicId,
      });

      try {
        const result = await exchangeShareFragment(fragment, controller.signal);

        if (String(result.status) === 'password_required') {
          setShareExchange({
            status: 'password',
            tone: 'warn',
            message: 'The backend requires a password before creating this share session.',
            publicId: fragment.publicId,
          });
          return;
        }

        clearShareFragment();
        setShareExchange({
          status: 'valid',
          tone: 'ok',
          message: 'Share session exchanged successfully; the address fragment was cleared.',
          publicId: result.publicId ?? fragment.publicId,
        });
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }

        if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
          setShareExchange({
            status: 'failed',
            tone: 'danger',
            message: 'The backend rejected this share link with the unified unavailable state.',
            publicId: fragment.publicId,
          });
          return;
        }

        setShareExchange({
          status: 'unavailable',
          tone: 'warn',
          message: 'Share exchange could not reach the backend; the visitor UI remains in demo-safe mode.',
          publicId: fragment.publicId,
        });
      }
    }

    void loadHealth();
    void loadBootstrap();
    void exchangeInitialShareFragment();

    return () => {
      controller.abort();
    };
  }, []);

  return (
    <div className="app-frame">
      <header className="topbar">
        <div className="brand">
          <div className="brand-mark" aria-hidden="true">O</div>
          <div>
            <p>Omnora / Wan Jing</p>
            <strong>NAS Console Preview</strong>
          </div>
        </div>
        <nav className="surface-switcher" aria-label="Application shell">
          {(['member', 'admin', 'share'] as Surface[]).map((item) => (
            <ShellButton key={item} surface={item} current={surface} onSelect={setSurface} />
          ))}
        </nav>
        <ConnectionPill health={health} bootstrap={bootstrap} />
      </header>

      <main>
        <section className="page-heading" aria-labelledby="surface-title">
          <div>
            <Badge tone={surface === 'admin' ? 'warn' : 'info'}>{copy.eyebrow}</Badge>
            <h1 id="surface-title">{copy.title}</h1>
            <p>{copy.description}</p>
          </div>
          <RouteGroupStrip routeGroups={appData.routeGroups} />
        </section>

        {surface === 'member' && <MemberShell data={appData} />}
        {surface === 'admin' && <AdminShell data={appData} />}
        {surface === 'share' && <ShareShell data={appData} shareExchange={shareExchange} onShareExchange={setShareExchange} />}
      </main>
    </div>
  );
}

function RouteGroupStrip({ routeGroups }: { routeGroups: AppBootstrap['routeGroups'] }) {
  return (
    <aside className="route-strip" aria-label="Route group exposure status">
      {routeGroups.map((route) => (
        <div className="route-chip" key={route.id}>
          <span>{route.label}</span>
          <Badge tone={route.tone}>{route.exposed ? route.entry : 'closed'}</Badge>
          <small>{route.risk}</small>
        </div>
      ))}
    </aside>
  );
}

function SessionPanel() {
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
  const [sessionSummary, setSessionSummary] = useState('No session check has run in this browser.');
	const [loginForm, setLoginForm] = useState({ login: 'admin@local', password: '', totpCode: '' });
	const [initForm, setInitForm] = useState({
		token: '',
		email: 'admin@local',
		displayName: 'Omnora Admin',
		password: '',
	});
	const [totpCode, setTotpCode] = useState('');
	const [totpSecret, setTotpSecret] = useState('');
	const [totpUri, setTotpUri] = useState('');

  async function onCheckSession() {
    setLoading(setFeedback, 'Checking session', 'GET /api/v1/auth/session');
    try {
      const session = await getSession();
      setSessionSummary(`Signed in as ${session.userId ?? 'unknown user'} until ${session.expiresAt ?? 'unknown expiry'}.`);
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Session active',
        detail: summarizeJson(session),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setSessionSummary('Demo fallback: no live session cookie was confirmed.');
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Session fallback',
          detail: apiErrorDetail(error),
        });
        return;
      }

      setSessionSummary('No valid session.');
      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Session unavailable',
        detail: apiErrorDetail(error),
      });
    }
  }

  async function onLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Logging in', 'POST /api/v1/auth/session');
    try {
      const session = await login(loginForm);
      setSessionSummary(`Signed in as ${session.userId ?? loginForm.login} until ${session.expiresAt ?? 'unknown expiry'}.`);
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Login accepted',
        detail: summarizeJson(session),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setSessionSummary(`Demo fallback: ${loginForm.login} is shown as locally signed in for preview.`);
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Login fallback',
          detail: apiErrorDetail(error),
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Login rejected',
        detail: apiErrorDetail(error),
      });
    }
  }

  async function onLogout() {
    setLoading(setFeedback, 'Logging out', 'DELETE /api/v1/auth/session');
    try {
      await logout();
      setSessionSummary('Session cookie revoked or was already absent.');
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Logged out',
        detail: 'The current browser session was revoked.',
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setSessionSummary('Demo fallback: local session display cleared.');
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Logout fallback',
          detail: apiErrorDetail(error),
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Logout failed',
        detail: apiErrorDetail(error),
      });
    }
  }

	async function onInitialize(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Initializing', 'POST /api/v1/initialize');
    try {
      const result = await initialize(initForm);
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Initialized',
        detail: summarizeJson(result),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Initialize fallback',
          detail: `Preview accepted first-admin details locally. ${apiErrorDetail(error)}`,
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Initialize rejected',
        detail: apiErrorDetail(error),
      });
		}
	}

	async function onSetupTOTP() {
		setLoading(setFeedback, 'Preparing TOTP', 'POST /api/v1/account/totp/setup');
		try {
			const result = await setupTOTP();
			setTotpSecret(result.secret ?? '');
			setTotpUri(result.otpauthUri ?? '');
			setFeedback({
				status: 'success',
				tone: 'ok',
				title: 'TOTP secret ready',
				detail: 'The backend returned the secret once; confirm a current code to enable it.',
			});
		} catch (error) {
			setFeedback({
				status: isMockFallbackError(error) ? 'mock' : 'error',
				tone: isMockFallbackError(error) ? 'warn' : 'danger',
				title: 'TOTP setup failed',
				detail: apiErrorDetail(error),
			});
		}
	}

	async function onConfirmTOTP() {
		setLoading(setFeedback, 'Confirming TOTP', 'POST /api/v1/account/totp/confirm');
		try {
			const result = await confirmTOTP(totpCode);
			setFeedback({
				status: 'success',
				tone: 'ok',
				title: 'TOTP enabled',
				detail: summarizeJson(result),
			});
		} catch (error) {
			setFeedback({
				status: isMockFallbackError(error) ? 'mock' : 'error',
				tone: isMockFallbackError(error) ? 'warn' : 'danger',
				title: 'TOTP confirm failed',
				detail: apiErrorDetail(error),
			});
		}
	}

	return (
    <section className="control-section operation-panel" aria-labelledby="session-title">
      <div className="panel-title">
        <span id="session-title">Initialization and session</span>
        <Badge tone="info">REST auth</Badge>
      </div>
      <p className="form-note">{sessionSummary}</p>
      <div className="form-grid">
        <form className="operation-form" onSubmit={onInitialize}>
          <label>
            <span>Initialization token</span>
            <input
              value={initForm.token}
              onChange={(event) => setInitForm({ ...initForm, token: event.target.value })}
              placeholder="one-time setup token"
              required
            />
          </label>
          <label>
            <span>Admin email</span>
            <input
              type="email"
              value={initForm.email}
              onChange={(event) => setInitForm({ ...initForm, email: event.target.value })}
              required
            />
          </label>
          <label>
            <span>Display name</span>
            <input
              value={initForm.displayName}
              onChange={(event) => setInitForm({ ...initForm, displayName: event.target.value })}
              required
            />
          </label>
          <label>
            <span>Admin password</span>
            <input
              type="password"
              value={initForm.password}
              onChange={(event) => setInitForm({ ...initForm, password: event.target.value })}
              autoComplete="new-password"
              required
            />
          </label>
          <button type="submit">Initialize</button>
        </form>

        <form className="operation-form" onSubmit={onLogin}>
          <label>
            <span>Login</span>
            <input
              value={loginForm.login}
              onChange={(event) => setLoginForm({ ...loginForm, login: event.target.value })}
              autoComplete="username"
              required
            />
          </label>
	          <label>
	            <span>Password</span>
            <input
              type="password"
              value={loginForm.password}
              onChange={(event) => setLoginForm({ ...loginForm, password: event.target.value })}
              autoComplete="current-password"
	              required
	            />
	          </label>
	          <label>
	            <span>TOTP code</span>
	            <input
	              inputMode="numeric"
	              value={loginForm.totpCode}
	              onChange={(event) => setLoginForm({ ...loginForm, totpCode: event.target.value })}
	              autoComplete="one-time-code"
	            />
	          </label>
	          <div className="inline-actions">
	            <button type="submit">Login</button>
	            <button type="button" onClick={onCheckSession}>Session status</button>
	            <button type="button" onClick={onLogout}>Logout</button>
	          </div>
	        </form>
	        <div className="operation-form">
	          <button type="button" onClick={onSetupTOTP}>Setup TOTP</button>
	          <label>
	            <span>Current code</span>
	            <input inputMode="numeric" value={totpCode} onChange={(event) => setTotpCode(event.target.value)} />
	          </label>
	          <button type="button" onClick={onConfirmTOTP}>Confirm TOTP</button>
	          {totpSecret && (
	            <div className="secret-output">
	              <span>TOTP secret shown once</span>
	              <code>{totpSecret}</code>
	            </div>
	          )}
	          {totpUri && (
	            <div className="secret-output">
	              <span>otpauth URI</span>
	              <code>{totpUri}</code>
	            </div>
	          )}
	        </div>
	      </div>
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function DirectoryBrowser({ data }: { data: AppBootstrap }) {
  const [form, setForm] = useState({
    spaceId: 'sp_personal',
    mountId: data.mounts[0]?.id ?? 'mnt_homevault',
    path: '/',
  });
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
  const [rows, setRows] = useState<DirectoryRow[]>(() => normalizeDirectoryRows({}, data.files));

  async function onBrowse(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Browsing directory', 'GET /api/v1/spaces/{spaceId}/mounts/{mountId}/children');
    try {
      const listing = await listDirectoryChildren(form.spaceId, form.mountId, form.path);
      setRows(normalizeDirectoryRows(listing, data.files));
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Directory loaded',
        detail: `Path ${listing.relativePath ?? listing.path ?? form.path}; ${listing.readOnly ? 'read-only' : 'write-capable'} response.`,
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setRows(normalizeDirectoryRows({}, data.files));
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Directory fallback',
          detail: `Showing bootstrap files while the children endpoint is unavailable. ${apiErrorDetail(error)}`,
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Browse failed',
        detail: apiErrorDetail(error),
      });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="browse-title">
      <div className="panel-title">
        <span id="browse-title">Directory path browsing</span>
        <Badge tone="info">children API</Badge>
      </div>
      <form className="operation-form operation-form-wide" onSubmit={onBrowse}>
        <label>
          <span>Space ID</span>
          <input value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required />
        </label>
        <label>
          <span>Mount ID</span>
          <input value={form.mountId} onChange={(event) => setForm({ ...form, mountId: event.target.value })} required />
        </label>
        <label>
          <span>Path</span>
          <input value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} placeholder="/" />
        </label>
        <button type="submit">Browse path</button>
      </form>
      <div className="mini-list" aria-label="Directory result preview">
        {rows.map((row) => (
          <div key={`${row.kind}-${row.name}`} className="mini-row">
            <strong>{row.name}</strong>
            <span>{row.kind} / {row.size}</span>
          </div>
        ))}
      </div>
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function ShareCreationForm({ selectedFile }: { selectedFile: FileItem | null }) {
  const [form, setForm] = useState({
    spaceId: 'sp_personal',
    mountId: 'mnt_homevault',
    relativePath: selectedFile?.name ?? '/',
    password: '',
    expiresAt: '',
    allowPreview: true,
    allowDownload: true,
    maxVisits: '20',
    maxDownloads: '',
  });
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
  const [shareUrl, setShareUrl] = useState('');

  useEffect(() => {
    if (selectedFile) {
      setForm((current) => ({ ...current, relativePath: selectedFile.name }));
    }
  }, [selectedFile]);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const payload: CreateSharePayload = {
      spaceId: form.spaceId,
      mountId: form.mountId,
      relativePath: form.relativePath,
      password: form.password || undefined,
      allowPreview: form.allowPreview,
      allowDownload: form.allowDownload,
      maxVisits: form.maxVisits ? Number(form.maxVisits) : undefined,
      maxDownloads: form.maxDownloads ? Number(form.maxDownloads) : undefined,
      expiresAt: form.expiresAt ? new Date(form.expiresAt).toISOString() : undefined,
    };

    setLoading(setFeedback, 'Creating share', 'POST /api/v1/shares');
    try {
      const result = await createShare(payload);
      const fragment = result.fragment ?? (result.publicId && (result.secret ?? result.fragmentSecret) ? `${result.publicId}.${result.secret ?? result.fragmentSecret}` : '');
      setShareUrl(fragment ? buildShareUrl(fragment) : '');
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Share created',
        detail: fragment ? 'One-time fragment URL generated from the backend response.' : summarizeJson(result),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        const fragment = `pub_demo.${crypto.randomUUID().replace(/-/g, '')}`;
        setShareUrl(buildShareUrl(fragment));
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Share fallback',
          detail: `Generated a demo fragment URL without contacting a backend. ${apiErrorDetail(error)}`,
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Share failed',
        detail: apiErrorDetail(error),
      });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="share-create-title">
      <div className="panel-title">
        <span id="share-create-title">Create share</span>
        <Badge tone="warn">secret shown once</Badge>
      </div>
      <form className="operation-form" onSubmit={onSubmit}>
        <label>
          <span>Space ID</span>
          <input value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required />
        </label>
        <label>
          <span>Mount ID</span>
          <input value={form.mountId} onChange={(event) => setForm({ ...form, mountId: event.target.value })} required />
        </label>
        <label>
          <span>Target path</span>
          <input value={form.relativePath} onChange={(event) => setForm({ ...form, relativePath: event.target.value })} required />
        </label>
        <label>
          <span>Password optional</span>
          <input type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} />
        </label>
        <label>
          <span>Expires at</span>
          <input type="datetime-local" value={form.expiresAt} onChange={(event) => setForm({ ...form, expiresAt: event.target.value })} />
        </label>
        <div className="check-row">
          <label><input type="checkbox" checked={form.allowPreview} onChange={(event) => setForm({ ...form, allowPreview: event.target.checked })} /> Preview</label>
          <label><input type="checkbox" checked={form.allowDownload} onChange={(event) => setForm({ ...form, allowDownload: event.target.checked })} /> Download</label>
        </div>
        <label>
          <span>Visit limit</span>
          <input type="number" min="1" value={form.maxVisits} onChange={(event) => setForm({ ...form, maxVisits: event.target.value })} />
        </label>
        <label>
          <span>Download limit</span>
          <input type="number" min="1" value={form.maxDownloads} onChange={(event) => setForm({ ...form, maxDownloads: event.target.value })} />
        </label>
        <button type="submit">Create share</button>
      </form>
      {shareUrl && (
        <div className="secret-output">
          <span>One-time URL</span>
          <code>{shareUrl}</code>
        </div>
      )}
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function AiTokenForm() {
  const [form, setForm] = useState({
    name: 'Local assistant',
    spaceId: 'sp_personal',
    mountId: 'mnt_homevault',
    path: '/',
    expiresAt: '',
    uploadsCreate: false,
  });
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
  const [secret, setSecret] = useState('');
  const [bearerToken, setBearerToken] = useState('');
  const [revokeId, setRevokeId] = useState('');
  const [tokenList, setTokenList] = useState('');

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const scopes = form.uploadsCreate ? aiScopes : aiScopes.filter((scope) => scope !== 'uploads:create');
    const payload: CreateAiTokenPayload = {
      name: form.name,
      scopes,
      boundaries: [{ spaceId: form.spaceId, mountId: form.mountId, path: form.path }],
      expiresAt: form.expiresAt ? new Date(form.expiresAt).toISOString() : new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString(),
    };

    setLoading(setFeedback, 'Creating AI Token', 'POST /api/v1/ai-tokens');
    try {
      const result = await createAiToken(payload);
      setSecret(result.bearerToken ?? result.secret ?? '');
      setBearerToken(result.bearerToken ?? '');
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'AI Token created',
        detail: result.secret ? 'Plaintext secret returned once.' : summarizeJson(result),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setSecret('omnora_demo_token_shown_once');
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Token placeholder',
          detail: `The form is wired to /api/v1/ai-tokens; current backend may not expose it yet. ${apiErrorDetail(error)}`,
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Token failed',
        detail: apiErrorDetail(error),
      });
    }
  }

  async function onListTokens() {
    setLoading(setFeedback, 'Listing AI Tokens', 'GET /api/v1/ai-tokens');
    try {
      const result = await listAiTokens();
      setTokenList(summarizeJson(result));
      setFeedback({ status: 'success', tone: 'ok', title: 'Tokens loaded', detail: summarizeJson(result) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Token list failed', detail: apiErrorDetail(error) });
    }
  }

  async function onRevokeToken() {
    if (!revokeId.trim()) {
      setFeedback({ status: 'error', tone: 'danger', title: 'Token ID required', detail: 'Enter the AI Token id to revoke.' });
      return;
    }
    setLoading(setFeedback, 'Revoking AI Token', 'DELETE /api/v1/ai-tokens/{tokenId}');
    try {
      await revokeAiToken(revokeId.trim());
      setFeedback({ status: 'success', tone: 'ok', title: 'Token revoked', detail: revokeId.trim() });
      await onListTokens();
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Token revoke failed', detail: apiErrorDetail(error) });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="ai-token-title">
      <div className="panel-title">
        <span id="ai-token-title">AI Token</span>
        <Badge tone="info">placeholder wired</Badge>
      </div>
      <form className="operation-form" onSubmit={onSubmit}>
        <label>
          <span>Name</span>
          <input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} required />
        </label>
        <label>
          <span>Space ID</span>
          <input value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required />
        </label>
        <label>
          <span>Mount ID</span>
          <input value={form.mountId} onChange={(event) => setForm({ ...form, mountId: event.target.value })} required />
        </label>
        <label>
          <span>Boundary path</span>
          <input value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} required />
        </label>
        <label>
          <span>Expires at</span>
          <input type="datetime-local" value={form.expiresAt} onChange={(event) => setForm({ ...form, expiresAt: event.target.value })} />
        </label>
        <label className="check-row single-check">
          <input type="checkbox" checked={form.uploadsCreate} onChange={(event) => setForm({ ...form, uploadsCreate: event.target.checked })} />
          Upload scope
        </label>
        <button type="submit">Create token</button>
      </form>
      {secret && (
        <div className="secret-output">
          <span>Plaintext token shown once</span>
          <code>{secret}</code>
        </div>
      )}
      <div className="operation-form">
        <div className="inline-actions">
          <button type="button" onClick={onListTokens}>List tokens</button>
        </div>
        <label>
          <span>Revoke token ID</span>
          <input value={revokeId} onChange={(event) => setRevokeId(event.target.value)} />
        </label>
        <button type="button" onClick={onRevokeToken}>Revoke token</button>
        {bearerToken && (
          <label>
            <span>MCP bearer token</span>
            <input value={bearerToken} onChange={(event) => setBearerToken(event.target.value)} />
          </label>
        )}
        {tokenList && (
          <div className="secret-output">
            <span>Token metadata</span>
            <code>{tokenList}</code>
          </div>
        )}
      </div>
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function UploadSessionForm() {
	const [form, setForm] = useState({
		spaceId: 'sp_personal',
		mountId: 'mnt_homevault',
		parentPath: '/inbox',
		fileName: 'example.txt',
		size: '1024',
	});
	const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
	const [uploadId, setUploadId] = useState('');
	const [uploadStatus, setUploadStatus] = useState('');
	const [selectedUploadFile, setSelectedUploadFile] = useState<File | null>(null);

	async function onSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const payload: CreateUploadPayload = {
			spaceId: form.spaceId,
			mountId: form.mountId,
			parentPath: form.parentPath,
			fileName: selectedUploadFile?.name ?? form.fileName,
			size: selectedUploadFile?.size ?? Number(form.size),
		};

    setLoading(setFeedback, 'Creating upload session', 'POST /api/v1/uploads');
		try {
			const result = await createUpload(payload);
			setUploadId(result.id ?? '');
			setUploadStatus(summarizeJson(result));
			setFeedback({
				status: 'success',
				tone: 'ok',
				title: 'Upload session created',
        detail: summarizeJson(result),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Upload placeholder',
          detail: `The form is wired to /api/v1/uploads; current backend may not expose it yet. ${apiErrorDetail(error)}`,
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Upload failed',
        detail: apiErrorDetail(error),
      });
		}
	}

	async function refreshUpload() {
		if (!uploadId) {
			setFeedback({ status: 'error', tone: 'danger', title: 'Upload ID required', detail: 'Create or enter an upload session ID first.' });
			return;
		}
		setLoading(setFeedback, 'Loading upload', 'GET /api/v1/uploads/{uploadId}');
		try {
			const result = await getUpload(uploadId);
			setUploadStatus(summarizeJson(result));
			setFeedback({ status: 'success', tone: 'ok', title: 'Upload status loaded', detail: summarizeJson(result) });
		} catch (error) {
			setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Upload status failed', detail: apiErrorDetail(error) });
		}
	}

	async function uploadNextPart() {
		if (!uploadId) {
			setFeedback({ status: 'error', tone: 'danger', title: 'Upload ID required', detail: 'Create or enter an upload session ID first.' });
			return;
		}
		setLoading(setFeedback, 'Uploading part', 'PUT /api/v1/uploads/{uploadId}/parts/{partNumber}');
		try {
			const current = await getUpload(uploadId);
			const partSize = current.partSize ?? 32768;
			const parts = current.parts ?? [];
			const nextPartNumber = parts.reduce((max, part) => Math.max(max, Number(part.Number ?? part.number ?? 0)), 0) + 1;
			const offset = (nextPartNumber - 1) * partSize;
			const chunk = selectedUploadFile ? selectedUploadFile.slice(offset, offset + partSize) : 'demo upload part';
			await uploadPart(uploadId, nextPartNumber, chunk);
			const updated = await getUpload(uploadId);
			setUploadStatus(summarizeJson(updated));
			setFeedback({ status: 'success', tone: 'ok', title: `Part ${nextPartNumber} uploaded`, detail: summarizeJson(updated) });
		} catch (error) {
			setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Part upload failed', detail: apiErrorDetail(error) });
		}
	}

	async function finishUpload() {
		if (!uploadId) {
			setFeedback({ status: 'error', tone: 'danger', title: 'Upload ID required', detail: 'Create or enter an upload session ID first.' });
			return;
		}
		setLoading(setFeedback, 'Completing upload', 'POST /api/v1/uploads/{uploadId}/complete');
		try {
			const result = await completeUpload(uploadId);
			setUploadStatus(summarizeJson(result));
			setFeedback({ status: 'success', tone: 'ok', title: 'Upload completed', detail: summarizeJson(result) });
		} catch (error) {
			setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Complete failed', detail: apiErrorDetail(error) });
		}
	}

	async function abortUpload() {
		if (!uploadId) {
			setFeedback({ status: 'error', tone: 'danger', title: 'Upload ID required', detail: 'Create or enter an upload session ID first.' });
			return;
		}
		setLoading(setFeedback, 'Canceling upload', 'DELETE /api/v1/uploads/{uploadId}');
		try {
			await cancelUpload(uploadId);
			setUploadStatus('Upload session canceled.');
			setFeedback({ status: 'success', tone: 'ok', title: 'Upload canceled', detail: uploadId });
		} catch (error) {
			setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Cancel failed', detail: apiErrorDetail(error) });
		}
	}

	return (
    <section className="control-section operation-panel" aria-labelledby="upload-title">
      <div className="panel-title">
        <span id="upload-title">Upload session</span>
        <Badge tone="info">placeholder wired</Badge>
      </div>
      <form className="operation-form" onSubmit={onSubmit}>
        <label>
          <span>Space ID</span>
          <input value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required />
        </label>
        <label>
          <span>Mount ID</span>
          <input value={form.mountId} onChange={(event) => setForm({ ...form, mountId: event.target.value })} required />
        </label>
        <label>
          <span>Parent path</span>
          <input value={form.parentPath} onChange={(event) => setForm({ ...form, parentPath: event.target.value })} required />
        </label>
				<label>
					<span>File name</span>
					<input value={form.fileName} onChange={(event) => setForm({ ...form, fileName: event.target.value })} required />
				</label>
				<label>
					<span>Local file</span>
					<input
						type="file"
						onChange={(event) => {
							const file = event.target.files?.[0] ?? null;
							setSelectedUploadFile(file);
							if (file) {
								setForm({ ...form, fileName: file.name, size: String(file.size) });
							}
						}}
					/>
				</label>
				<label>
					<span>Size bytes</span>
					<input type="number" min="0" value={form.size} onChange={(event) => setForm({ ...form, size: event.target.value })} required />
				</label>
				<button type="submit">Create upload</button>
			</form>
			<div className="operation-form">
				<label>
					<span>Upload ID</span>
					<input value={uploadId} onChange={(event) => setUploadId(event.target.value)} />
				</label>
				<div className="inline-actions">
					<button type="button" onClick={refreshUpload}>Resume status</button>
					<button type="button" onClick={uploadNextPart}>Upload next part</button>
					<button type="button" onClick={finishUpload}>Complete</button>
					<button type="button" onClick={abortUpload}>Cancel</button>
				</div>
				{uploadStatus && (
					<div className="secret-output">
						<span>Upload state</span>
						<code>{uploadStatus}</code>
					</div>
				)}
			</div>
			<OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
		</section>
	);
}

function SearchAndDownloadForm() {
  const [form, setForm] = useState({
    spaceId: 'sp_personal',
    mountId: 'mnt_homevault',
    path: 'example.txt',
    query: '.txt',
    range: 'bytes=0-127',
  });
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
  const [result, setResult] = useState('');

  async function onSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Searching index', 'GET /api/v1/spaces/{spaceId}/search');
    try {
      const payload = await searchSpace(form.spaceId, form.query);
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'Search completed', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Search failed', detail: apiErrorDetail(error) });
    }
  }

  async function onRangeDownload() {
    setLoading(setFeedback, 'Downloading range', 'GET /api/v1/spaces/{spaceId}/mounts/{mountId}/download');
    try {
      const payload = await downloadRange(form.spaceId, form.mountId, form.path, form.range);
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'Range downloaded', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Download failed', detail: apiErrorDetail(error) });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="search-download-title">
      <div className="panel-title">
        <span id="search-download-title">Search and download</span>
        <Badge tone="info">live metadata</Badge>
      </div>
      <form className="operation-form" onSubmit={onSearch}>
        <label>
          <span>Space ID</span>
          <input value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required />
        </label>
        <label>
          <span>Search query</span>
          <input value={form.query} onChange={(event) => setForm({ ...form, query: event.target.value })} required />
        </label>
        <button type="submit">Search</button>
      </form>
      <div className="operation-form">
        <label>
          <span>Mount ID</span>
          <input value={form.mountId} onChange={(event) => setForm({ ...form, mountId: event.target.value })} required />
        </label>
        <label>
          <span>File path</span>
          <input value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} required />
        </label>
        <label>
          <span>Range header</span>
          <input value={form.range} onChange={(event) => setForm({ ...form, range: event.target.value })} />
        </label>
        <button type="button" onClick={onRangeDownload}>Download range</button>
      </div>
      {result && (
        <div className="secret-output">
          <span>Response</span>
          <code>{result}</code>
        </div>
      )}
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function MCPForm() {
  const [form, setForm] = useState({
    bearerToken: '',
    method: 'tools/list',
    query: '',
  });
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);
  const [result, setResult] = useState('');

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const params = form.method === 'files.search' ? { q: form.query } : {};
    setLoading(setFeedback, 'Calling MCP', 'POST /mcp');
    try {
      const payload = await callMCP(form.bearerToken, form.method, params);
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'MCP response', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'MCP failed', detail: apiErrorDetail(error) });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="mcp-title">
      <div className="panel-title">
        <span id="mcp-title">MCP</span>
        <Badge tone="info">AI token</Badge>
      </div>
      <form className="operation-form" onSubmit={onSubmit}>
        <label>
          <span>Bearer token</span>
          <input value={form.bearerToken} onChange={(event) => setForm({ ...form, bearerToken: event.target.value })} required />
        </label>
        <label>
          <span>Method</span>
          <select value={form.method} onChange={(event) => setForm({ ...form, method: event.target.value })}>
            <option value="tools/list">tools/list</option>
            <option value="spaces.list">spaces.list</option>
            <option value="files.search">files.search</option>
          </select>
        </label>
        <label>
          <span>Search query</span>
          <input value={form.query} onChange={(event) => setForm({ ...form, query: event.target.value })} />
        </label>
        <button type="submit">Call MCP</button>
      </form>
      {result && (
        <div className="secret-output">
          <span>MCP response</span>
          <code>{result}</code>
        </div>
      )}
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function MemberShell({ data }: { data: AppBootstrap }) {
  const [activeNav, setActiveNav] = useState('Files');
  const [selectedId, setSelectedId] = useState(data.files[0]?.id ?? '');
  const [focusedIndex, setFocusedIndex] = useState(0);
  const rowsRef = useRef<Array<HTMLTableRowElement | null>>([]);
  const selectedFile = useMemo(
    () => data.files.find((file) => file.id === selectedId) ?? data.files[0] ?? null,
    [data.files, selectedId],
  );

  useEffect(() => {
    if (data.files.length > 0 && !data.files.some((file) => file.id === selectedId)) {
      setSelectedId(data.files[0].id);
      setFocusedIndex(0);
    }
  }, [data.files, selectedId]);

  function moveFocus(nextIndex: number) {
    const bounded = Math.max(0, Math.min(data.files.length - 1, nextIndex));
    setFocusedIndex(bounded);
    rowsRef.current[bounded]?.focus();
  }

  function onFileKeyDown(event: KeyboardEvent<HTMLTableRowElement>, index: number, item: FileItem) {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      moveFocus(index + 1);
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      moveFocus(index - 1);
    }
    if (event.key === 'Home') {
      event.preventDefault();
      moveFocus(0);
    }
    if (event.key === 'End') {
      event.preventDefault();
      moveFocus(data.files.length - 1);
    }
    if (event.key === ' ' || event.key === 'Enter') {
      event.preventDefault();
      setSelectedId(item.id);
    }
  }

  return (
    <section className="member-grid">
      <nav className="rail" aria-label="Member navigation">
        {['Files', 'Search', 'Transfers', 'Shares', 'AI Token', 'Account'].map((item) => (
          <button key={item} type="button" aria-pressed={activeNav === item} onClick={() => setActiveNav(item)}>
            <span aria-hidden="true">{item.slice(0, 2)}</span>
            <strong>{item}</strong>
          </button>
        ))}
      </nav>

      <aside className="secondary-panel" aria-label="Spaces and mounts">
        <div className="panel-title">
          <span>Spaces</span>
          <Badge tone="info">manager</Badge>
        </div>
        {data.mounts.map((mount) => (
          <button className="mount-row" key={mount.name} type="button">
            <strong>{mount.name}</strong>
            <span>{mount.space}</span>
            <div>
              <Badge tone={mount.mode === 'read-write' ? 'ok' : 'warn'}>{mount.mode}</Badge>
              <Badge tone={mount.tone}>{mount.health}</Badge>
            </div>
          </button>
        ))}
        <div className="notice notice-warn">
          Space managers can manage ACL and shares here, but mount configuration stays in the admin control plane.
        </div>
      </aside>

      <section className="workspace" aria-labelledby="workspace-title">
        <div className="toolbar">
          <div>
            <p className="crumb">Li Personal / HomeVault / Documents</p>
            <h2 id="workspace-title">Files</h2>
          </div>
          <div className="actions" aria-label="File actions">
            <button type="button">Upload</button>
            <button type="button">New folder</button>
            <button type="button" disabled title="Disabled because the selected mount is read-only or blocked">
              Delete
            </button>
            <button type="button">More</button>
          </div>
        </div>

        <div className="filter-row" role="search">
          <label>
            <span>Search name</span>
            <input type="search" placeholder="filename, extension, folder" />
          </label>
          <label>
            <span>Type</span>
            <select defaultValue="all">
              <option value="all">All types</option>
              <option value="preview">Previewable</option>
              <option value="office">Office download only</option>
            </select>
          </label>
          <Badge tone="warn">OldDrive excluded: identity drift</Badge>
        </div>

        <DirectoryBrowser data={data} />

        <div className="table-wrap" role="region" aria-label="File list" tabIndex={-1}>
          <table className="data-table">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Size</th>
                <th scope="col">Modified</th>
                <th scope="col">Mount</th>
                <th scope="col">State</th>
              </tr>
            </thead>
            <tbody>
              {data.files.map((item, index) => (
                <tr
                  key={item.id}
                  ref={(node) => {
                    rowsRef.current[index] = node;
                  }}
                  tabIndex={focusedIndex === index ? 0 : -1}
                  aria-selected={selectedId === item.id}
                  onClick={() => setSelectedId(item.id)}
                  onFocus={() => setFocusedIndex(index)}
                  onKeyDown={(event) => onFileKeyDown(event, index, item)}
                >
                  <td>
                    <div className="file-name">
                      <span className={`file-glyph glyph-${item.kind}`} aria-hidden="true">
                        {item.kind.slice(0, 1).toUpperCase()}
                      </span>
                      <span>{item.name}</span>
                    </div>
                  </td>
                  <td>{item.size}</td>
                  <td>{item.modified}</td>
                  <td>
                    <Badge tone={item.access === 'read-write' ? 'ok' : 'warn'}>{item.mount}</Badge>
                  </td>
                  <td>
                    <Badge tone={item.statusTone}>{item.status}</Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {data.files.length === 0 && (
            <div className="empty-state table-empty">
              <h2>No files returned</h2>
              <p>The backend bootstrap returned an empty file set for this view.</p>
            </div>
          )}
        </div>
        <button className="load-more" type="button">Load next 200 items</button>

        <div className="operation-stack">
					<SessionPanel />
					<AiTokenForm />
					<UploadSessionForm />
					<SearchAndDownloadForm />
					<MCPForm />
				</div>
      </section>

      <aside className="details-panel" aria-label="Object details">
        <div className="panel-title">
          <span>Details</span>
          {selectedFile ? <Badge tone={selectedFile.statusTone}>{selectedFile.status}</Badge> : <Badge>empty</Badge>}
        </div>
        {selectedFile ? (
          <>
            <h3>{selectedFile.name}</h3>
            <dl className="kv-list">
              <div><dt>Mount</dt><dd>{selectedFile.mount}</dd></div>
              <div><dt>Mode</dt><dd>{selectedFile.access}</dd></div>
              <div><dt>Index</dt><dd>{selectedFile.index}</dd></div>
              <div><dt>Preview</dt><dd>{selectedFile.kind === 'office' ? 'Office files download only' : 'Requires backend recheck'}</dd></div>
            </dl>
            <div className="button-stack">
              <button type="button">Preview</button>
              <button type="button">Create share</button>
              <button type="button" disabled>Permanent delete</button>
            </div>
            <ShareCreationForm selectedFile={selectedFile} />
          </>
        ) : (
          <div className="empty-state">
            <h3>No object selected</h3>
            <p>There is no file metadata available from the current bootstrap payload.</p>
          </div>
        )}
      </aside>

      <TransferBar transfers={data.transfers} />
    </section>
  );
}

function TransferBar({ transfers }: { transfers: AppBootstrap['transfers'] }) {
  return (
    <aside className="transfer-bar" aria-label="Transfer center">
      {transfers.map((transfer) => (
        <div className="transfer-item" key={transfer.name}>
          <div>
            <strong>{transfer.name}</strong>
            <span>{transfer.operation}</span>
          </div>
          <Meter value={transfer.progress} label={transfer.name} />
          <Badge tone={transfer.tone}>{transfer.state}</Badge>
        </div>
      ))}
    </aside>
  );
}

function AdminMountRegistrationForm() {
  const [form, setForm] = useState({
    spaceId: 'sp_personal',
    displayName: 'MediaArchive',
    rootPath: '/data/media',
    kind: 'external' as 'external' | 'managed',
    mode: 'read_only' as 'read_only' | 'read_write',
    indexEnabled: true,
  });
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Registering mount', 'POST /api/v1/admin/mounts');
    try {
      const result = await registerAdminMount(form);
      setFeedback({
        status: 'success',
        tone: 'ok',
        title: 'Mount registered',
        detail: summarizeJson(result),
      });
    } catch (error) {
      if (isMockFallbackError(error)) {
        setFeedback({
          status: 'mock',
          tone: 'warn',
          title: 'Mount fallback',
          detail: `Preview accepted a pre-mapped container path locally. ${apiErrorDetail(error)}`,
        });
        return;
      }

      setFeedback({
        status: 'error',
        tone: 'danger',
        title: 'Mount rejected',
        detail: apiErrorDetail(error),
      });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="admin-mount-title">
      <div className="panel-title">
        <span id="admin-mount-title">Register mount</span>
        <Badge tone="warn">admin only</Badge>
      </div>
      <form className="operation-form operation-form-wide" onSubmit={onSubmit}>
        <label>
          <span>Space ID</span>
          <input value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required />
        </label>
        <label>
          <span>Display name</span>
          <input value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })} required />
        </label>
        <label>
          <span>Container path</span>
          <input value={form.rootPath} onChange={(event) => setForm({ ...form, rootPath: event.target.value })} required />
        </label>
        <label>
          <span>Kind</span>
          <select value={form.kind} onChange={(event) => setForm({ ...form, kind: event.target.value as 'external' | 'managed' })}>
            <option value="external">External</option>
            <option value="managed">Managed</option>
          </select>
        </label>
        <label>
          <span>Mode</span>
          <select value={form.mode} onChange={(event) => setForm({ ...form, mode: event.target.value as 'read_only' | 'read_write' })}>
            <option value="read_only">Read only</option>
            <option value="read_write">Read write</option>
          </select>
        </label>
        <label className="check-row single-check">
          <input type="checkbox" checked={form.indexEnabled} onChange={(event) => setForm({ ...form, indexEnabled: event.target.checked })} />
          Include in index
        </label>
        <button type="submit">Register mount</button>
      </form>
      <p className="form-note">Paths must already be mapped inside the Omnora container; the backend rejects overlaps, symlinks, and unverifiable identities.</p>
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
	);
}

function IndexJobForm() {
  const [mountId, setMountId] = useState('mnt_homevault');
  const [jobId, setJobId] = useState('');
  const [result, setResult] = useState('');
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);

  async function onEnqueue(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Enqueueing index job', 'POST /api/v1/admin/index-jobs');
    try {
      const payload = await enqueueIndexJob(mountId);
      setJobId(payload.id ?? jobId);
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'Index job queued', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Index enqueue failed', detail: apiErrorDetail(error) });
    }
  }

  async function onListJobs() {
    setLoading(setFeedback, 'Listing index jobs', 'GET /api/v1/admin/index-jobs');
    try {
      const payload = await listIndexJobs();
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'Jobs loaded', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Job list failed', detail: apiErrorDetail(error) });
    }
  }

  async function onRunJob() {
    if (!jobId.trim()) {
      setFeedback({ status: 'error', tone: 'danger', title: 'Job ID required', detail: 'Enter a queued job ID before running it.' });
      return;
    }
    setLoading(setFeedback, 'Running index job', 'POST /api/v1/admin/index-jobs/{jobId}/run');
    try {
      const payload = await runIndexJob(jobId.trim());
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'Job run finished', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Job run failed', detail: apiErrorDetail(error) });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="index-job-title">
      <div className="panel-title">
        <span id="index-job-title">Index jobs</span>
        <Badge tone="warn">admin only</Badge>
      </div>
      <form className="operation-form" onSubmit={onEnqueue}>
        <label>
          <span>Mount ID</span>
          <input value={mountId} onChange={(event) => setMountId(event.target.value)} required />
        </label>
        <button type="submit">Queue scan</button>
      </form>
      <div className="operation-form">
        <label>
          <span>Job ID</span>
          <input value={jobId} onChange={(event) => setJobId(event.target.value)} />
        </label>
        <div className="inline-actions">
          <button type="button" onClick={onListJobs}>List jobs</button>
          <button type="button" onClick={onRunJob}>Run job</button>
        </div>
        {result && (
          <div className="secret-output">
            <span>Job response</span>
            <code>{result}</code>
          </div>
        )}
      </div>
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function AuditGovernanceForm() {
  const [limit, setLimit] = useState('50');
  const [result, setResult] = useState('');
  const [feedback, setFeedback] = useState<OperationFeedback>(idleFeedback);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(setFeedback, 'Loading audit events', 'GET /api/v1/audit/events');
    try {
      const payload = await listAuditEvents(Number(limit));
      setResult(summarizeJson(payload));
      setFeedback({ status: 'success', tone: 'ok', title: 'Audit loaded', detail: summarizeJson(payload) });
    } catch (error) {
      setFeedback({ status: isMockFallbackError(error) ? 'mock' : 'error', tone: isMockFallbackError(error) ? 'warn' : 'danger', title: 'Audit query failed', detail: apiErrorDetail(error) });
    }
  }

  return (
    <section className="control-section operation-panel" aria-labelledby="audit-query-title">
      <div className="panel-title">
        <span id="audit-query-title">Audit query</span>
        <Badge tone="warn">governance</Badge>
      </div>
      <form className="operation-form" onSubmit={onSubmit}>
        <label>
          <span>Limit</span>
          <input type="number" min="1" max="200" value={limit} onChange={(event) => setLimit(event.target.value)} />
        </label>
        <button type="submit">Load audit</button>
      </form>
      {result && (
        <div className="secret-output">
          <span>Audit events</span>
          <code>{result}</code>
        </div>
      )}
      <OperationStatus feedback={feedback} onClear={() => setFeedback(idleFeedback)} />
    </section>
  );
}

function AdminShell({ data }: { data: AppBootstrap }) {
  const [active, setActive] = useState('Overview');
  const adminNav = ['Overview', 'Members', 'Spaces', 'Mounts', 'Index', 'Network', 'Route groups', 'Governance', 'Audit', 'Backup'];

  return (
    <section className="admin-grid">
      <nav className="console-nav" aria-label="Admin navigation">
        {adminNav.map((item) => (
          <button key={item} type="button" aria-pressed={active === item} onClick={() => setActive(item)}>
            {item}
          </button>
        ))}
      </nav>

      <section className="admin-main" aria-labelledby="admin-title">
        <div className="toolbar">
          <div>
            <p className="crumb">Control plane / {active}</p>
            <h2 id="admin-title">Overview and controls</h2>
          </div>
          <div className="actions">
            <button type="button">Re-authenticate</button>
            <button type="button">Pause background task</button>
          </div>
        </div>

        <div className="stat-grid">
          {data.adminRisks.map((risk) => (
            <div className="stat-tile" key={risk.label}>
              <span>{risk.label}</span>
              <strong>{risk.value}</strong>
              <Badge tone={risk.tone}>{risk.tone}</Badge>
            </div>
          ))}
        </div>

				<div className="admin-operations">
					<SessionPanel />
					<AdminMountRegistrationForm />
					<IndexJobForm />
					<AuditGovernanceForm />
				</div>

        <div className="split-content">
          <section className="control-section" aria-labelledby="mount-health-title">
            <div className="panel-title">
              <span id="mount-health-title">Mount health</span>
              <Badge tone="info">admin only</Badge>
            </div>
            {data.mounts.map((mount) => (
              <div className="control-row" key={mount.name}>
                <div>
                  <strong>{mount.name}</strong>
                  <span>{mount.space} / {mount.mode}</span>
                </div>
                <Badge tone={mount.tone}>{mount.health}</Badge>
                <button type="button">Revalidate</button>
              </div>
            ))}
          </section>

          <section className="control-section" aria-labelledby="routes-title">
            <div className="panel-title">
              <span id="routes-title">Route groups</span>
              <Badge tone="warn">entry + exposure required</Badge>
            </div>
            {data.routeGroups.map((route) => (
              <label className="toggle-row" key={route.id}>
                <input type="checkbox" defaultChecked={route.exposed} />
                <span>{route.label}</span>
                <Badge tone={route.tone}>{route.risk}</Badge>
              </label>
            ))}
          </section>
        </div>

        <div className="table-wrap" role="region" aria-label="Audit log">
          <table className="data-table">
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Actor</th>
                <th scope="col">Event</th>
                <th scope="col">Target</th>
                <th scope="col">Result</th>
              </tr>
            </thead>
            <tbody>
              {data.auditRows.map((row) => (
                <tr key={`${row.time}-${row.event}`}>
                  <td>{row.time}</td>
                  <td>{row.actor}</td>
                  <td>{row.event}</td>
                  <td>{row.target}</td>
                  <td>{row.result}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="notice notice-info">
          Admin overview intentionally shows capacity, health, and audit metadata only; personal file names stay out of the control plane.
        </div>
      </section>
    </section>
  );
}

function ShareShell({
  data,
  shareExchange,
  onShareExchange,
}: {
  data: AppBootstrap;
  shareExchange: ShareExchangeState;
  onShareExchange: (state: ShareExchangeState) => void;
}) {
  const [shareState, setShareState] = useState<ShareState>(() => mapShareExchangeToState(shareExchange));
  const initialFragment = parseShareFragment();
  const [exchangeForm, setExchangeForm] = useState({
    publicId: initialFragment?.publicId ?? shareExchange.publicId ?? '',
    secret: initialFragment?.secret ?? '',
    password: '',
  });
  const stateTone: Record<ShareState, Tone> = {
    valid: 'ok',
    password: 'warn',
    missing: 'danger',
    expired: 'muted',
    unavailable: 'warn',
  };

  useEffect(() => {
    setShareState(mapShareExchangeToState(shareExchange));
  }, [shareExchange]);

  async function onExchange(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const fragment: ShareFragment = {
      publicId: exchangeForm.publicId.trim(),
      secret: exchangeForm.secret.trim(),
    };

    if (!fragment.publicId || !fragment.secret) {
      onShareExchange({
        status: 'missing',
        tone: 'danger',
        message: 'Public ID and fragment secret are required before a share-session request can be sent.',
      });
      return;
    }

    onShareExchange({
      status: 'exchanging',
      tone: 'info',
      message: `Exchanging ${fragment.publicId} with /api/v1/share-sessions.`,
      publicId: fragment.publicId,
    });

    try {
      const result = await exchangeShareFragmentWithPassword(fragment, exchangeForm.password);

      if (String(result.status) === 'password_required') {
        onShareExchange({
          status: 'password',
          tone: 'warn',
          message: 'Password is required; submit again with the password to create a share session.',
          publicId: fragment.publicId,
        });
        setShareState('password');
        return;
      }

      clearShareFragment();
      setExchangeForm((current) => ({ ...current, secret: '', password: '' }));
      onShareExchange({
        status: 'valid',
        tone: 'ok',
        message: `Share session created${result.expiresAt ? ` until ${result.expiresAt}` : ''}; fragment cleared from the address bar.`,
        publicId: result.publicId ?? fragment.publicId,
      });
      setShareState('valid');
    } catch (error) {
      if (isMockFallbackError(error)) {
        onShareExchange({
          status: 'unavailable',
          tone: 'warn',
          message: `Demo-safe fallback: no share session was created. ${apiErrorDetail(error)}`,
          publicId: fragment.publicId,
        });
        setShareState('unavailable');
        return;
      }

      onShareExchange({
        status: 'failed',
        tone: 'danger',
        message: `Unified unavailable state: ${apiErrorDetail(error)}`,
        publicId: fragment.publicId,
      });
      setShareState('expired');
    }
  }

  return (
    <section className="share-layout">
      <div className="share-state-tabs" role="tablist" aria-label="Visitor state">
        {(['valid', 'password', 'missing', 'expired', 'unavailable'] as ShareState[]).map((state) => (
          <button
            key={state}
            type="button"
            role="tab"
            aria-selected={shareState === state}
            onClick={() => setShareState(state)}
          >
            {state}
          </button>
        ))}
      </div>

      <section className="share-panel" aria-labelledby="share-title">
        <div className="panel-title">
          <span id="share-title">Shared folder</span>
          <Badge tone={stateTone[shareState]}>{shareState}</Badge>
        </div>
        <div className="notice notice-info share-exchange-status">
          <Badge tone={shareExchange.tone}>{shareExchange.status}</Badge>
          <span>{shareExchange.message}</span>
        </div>

        <form className="password-box share-exchange-form" onSubmit={onExchange}>
          <label>
            <span>Public ID</span>
            <input
              value={exchangeForm.publicId}
              onChange={(event) => setExchangeForm({ ...exchangeForm, publicId: event.target.value })}
              placeholder="pub_..."
            />
          </label>
          <label>
            <span>Fragment secret</span>
            <input
              type="password"
              value={exchangeForm.secret}
              onChange={(event) => setExchangeForm({ ...exchangeForm, secret: event.target.value })}
              autoComplete="off"
              placeholder="from #public.secret"
            />
          </label>
          <label>
            <span>Password optional</span>
            <input
              type="password"
              value={exchangeForm.password}
              onChange={(event) => setExchangeForm({ ...exchangeForm, password: event.target.value })}
              autoComplete="current-password"
            />
          </label>
          <button type="submit">Exchange share session</button>
        </form>

        {shareState === 'valid' && (
          <>
            <div className="share-object">
              <span className="file-glyph glyph-folder" aria-hidden="true">F</span>
              <div>
                <h2>Trip photos / Selected exports</h2>
                <p>Preview enabled, download disabled. Displayed content may still be saved by visitors.</p>
              </div>
            </div>
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>
                    <th scope="col">Name</th>
                    <th scope="col">Size</th>
                    <th scope="col">Capability</th>
                  </tr>
                </thead>
                <tbody>
                  {data.shares.map((share) => (
                    <tr key={share.id}>
                      <td>{share.target}</td>
                      <td>{share.use}</td>
                      <td><Badge tone={share.tone}>{share.capability}</Badge></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}

        {shareState === 'password' && (
          <div className="empty-state">
            <h2>Password required</h2>
            <p>Password-required responses do not create a share session or consume visit count.</p>
          </div>
        )}

        {shareState === 'missing' && (
          <div className="empty-state">
            <h2>Share secret missing</h2>
            <p>The frontend would not submit a request because no URL fragment secret is available in browser memory.</p>
          </div>
        )}

        {shareState === 'unavailable' && (
          <div className="empty-state">
            <h2>Backend unavailable</h2>
            <p>The share secret stayed in browser memory, but no session was created because the API could not be reached.</p>
          </div>
        )}

        {shareState === 'expired' && (
          <div className="empty-state">
            <h2>This share is unavailable</h2>
            <p>Visitors see a unified failure state for expired, revoked, exhausted, moved, or invalid shares.</p>
          </div>
        )}
      </section>

      <aside className="share-side" aria-label="Share safety model">
        <h3>Visitor guardrails</h3>
        <ul>
          <li>Secret belongs in the URL fragment, never path or query.</li>
          <li>Successful exchange clears the address fragment.</li>
          <li>Refresh within the same share session does not consume another visit.</li>
          <li>Download tickets pre-consume download count.</li>
        </ul>
      </aside>

      <section className="control-section share-management" aria-labelledby="member-shares-title">
        <div className="panel-title">
          <span id="member-shares-title">Member shares and AI tokens</span>
          <Badge tone="info">metadata only</Badge>
        </div>
        <div className="governance-list">
          {data.shares.map((share) => (
            <div className="control-row" key={share.id}>
              <div>
                <strong>{share.id}</strong>
                <span>{share.target} / expires {share.expires}</span>
              </div>
              <Badge tone={share.tone}>{share.status}</Badge>
              <button type="button">Revoke</button>
            </div>
          ))}
          {data.tokens.map((token) => (
            <div className="control-row" key={token.id}>
              <div>
                <strong>{token.name}</strong>
                <span>{token.boundary} / {token.scope}</span>
              </div>
              <Badge tone={token.tone}>{token.status}</Badge>
              <button type="button">Revoke</button>
            </div>
          ))}
        </div>
      </section>
    </section>
  );
}

export default App;
