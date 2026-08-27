import { useCallback, useEffect, useState } from 'react';
import { ApiError, getBootstrap, isReauthenticationCanceled } from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import MemberDocsNavigation from './MemberDocsNavigation';
import McpDocsBlock from './McpDocsBlock';
import { getMcpRouteState } from './mcpIntegration';

type LocaleText = (typeof localeMessages)[MemberLocale];

export type DocsTab = 'mcp' | 'openapi' | 'faq';

type OpenApiEndpoint = {
  method: string;
  path: string;
  desc: keyof LocaleText;
  auth: 'none' | 'session' | 'bearer';
};

type OpenApiGroup = {
  title: keyof LocaleText;
  endpoints: OpenApiEndpoint[];
};

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

// Curated REST surface, kept in sync with openapi/omnora.v1.yaml and
// docs/api/README.md. The full contract stays authoritative; this table is a
// quick reference for browser users.
const OPENAPI_GROUPS: OpenApiGroup[] = [
  {
    title: 'docsOpenapiGroupSystem',
    endpoints: [
      { method: 'GET', path: '/healthz', desc: 'docsOpenapiHealthz', auth: 'none' },
      { method: 'GET', path: '/readyz', desc: 'docsOpenapiReadyz', auth: 'none' },
      { method: 'GET / POST', path: '/mcp', desc: 'docsOpenapiMcp', auth: 'bearer' },
    ],
  },
  {
    title: 'docsOpenapiGroupIdentity',
    endpoints: [
      { method: 'POST', path: '/api/v1/auth/session', desc: 'docsOpenapiSessionPost', auth: 'none' },
      { method: 'GET', path: '/api/v1/auth/session', desc: 'docsOpenapiSessionGet', auth: 'session' },
      { method: 'DELETE', path: '/api/v1/auth/session', desc: 'docsOpenapiSessionDelete', auth: 'session' },
      { method: 'POST', path: '/api/v1/account/reauthenticate', desc: 'docsOpenapiReauth', auth: 'session' },
      { method: 'POST', path: '/api/v1/account/totp/setup', desc: 'docsOpenapiTotpSetup', auth: 'session' },
      { method: 'POST', path: '/api/v1/account/totp/confirm', desc: 'docsOpenapiTotpConfirm', auth: 'session' },
    ],
  },
  {
    title: 'docsOpenapiGroupContent',
    endpoints: [
      { method: 'GET', path: '/api/v1/member/content-sources', desc: 'docsOpenapiContentSources', auth: 'session' },
      { method: 'GET', path: '/api/v1/member/files/children', desc: 'docsOpenapiChildren', auth: 'session' },
      { method: 'GET', path: '/api/v1/member/files/download', desc: 'docsOpenapiDownload', auth: 'session' },
      { method: 'GET', path: '/api/v1/member/files/search', desc: 'docsOpenapiSearch', auth: 'session' },
      { method: 'POST', path: '/api/v1/member/files/directories', desc: 'docsOpenapiDirectoryCreate', auth: 'session' },
      { method: 'POST', path: '/api/v1/member/files/rename', desc: 'docsOpenapiFileMutation', auth: 'session' },
      { method: 'POST', path: '/api/v1/member/files/move', desc: 'docsOpenapiFileMutation', auth: 'session' },
      { method: 'POST', path: '/api/v1/member/files/copy', desc: 'docsOpenapiFileMutation', auth: 'session' },
      { method: 'DELETE', path: '/api/v1/member/files/object', desc: 'docsOpenapiFileMutation', auth: 'session' },
    ],
  },
  {
    title: 'docsOpenapiGroupSharesTokens',
    endpoints: [
      { method: 'GET / POST', path: '/api/v1/shares', desc: 'docsOpenapiShares', auth: 'session' },
      { method: 'GET / POST', path: '/api/v1/ai-tokens', desc: 'docsOpenapiAiTokens', auth: 'session' },
    ],
  },
  {
    title: 'docsOpenapiGroupAdmin',
    endpoints: [
      { method: 'GET', path: '/route-groups', desc: 'docsOpenapiRouteGroups', auth: 'session' },
      { method: 'GET / POST', path: '/api/v1/admin/users', desc: 'docsOpenapiAdminUsers', auth: 'session' },
      { method: 'GET / POST', path: '/api/v1/admin/mounts', desc: 'docsOpenapiAdminMounts', auth: 'session' },
      { method: 'GET / POST', path: '/api/v1/admin/index-jobs', desc: 'docsOpenapiAdminJobs', auth: 'session' },
      { method: 'GET', path: '/api/v1/audit/events', desc: 'docsOpenapiAudit', auth: 'session' },
    ],
  },
];

const FAQ_ITEMS: Array<{ q: keyof LocaleText; a: keyof LocaleText }> = [
  { q: 'docsFaqQ1', a: 'docsFaqA1' },
  { q: 'docsFaqQ2', a: 'docsFaqA2' },
  { q: 'docsFaqQ3', a: 'docsFaqA3' },
  { q: 'docsFaqQ4', a: 'docsFaqA4' },
  { q: 'docsFaqQ5', a: 'docsFaqA5' },
  { q: 'docsFaqQ6', a: 'docsFaqA6' },
  { q: 'docsFaqQ7', a: 'docsFaqA7' },
];

function authLabel(auth: OpenApiEndpoint['auth'], text: LocaleText) {
  if (auth === 'none') return text.docsOpenapiAuthNone;
  if (auth === 'bearer') return text.docsOpenapiAuthBearer;
  return text.docsOpenapiAuthSession;
}

export default function MemberDocsPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [tab, setTab] = useState<DocsTab>('mcp');
  const [bootstrap, setBootstrap] = useState<Awaited<ReturnType<typeof getBootstrap>> | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const response = await getBootstrap();
      setBootstrap(response);
    } catch (caught) {
      if (!isReauthenticationCanceled(caught)) setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const routeState = getMcpRouteState(bootstrap);
  const openapiUrl = `${(typeof globalThis.location?.origin === 'string' ? globalThis.location.origin : '').replace(/\/+$/, '')}/openapi/omnora.v1.yaml`;

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.docsTitle}</h1><p>{text.docsTitleDetail}</p></div>
      </div>

      <div className="member-docs-layout">
        <MemberDocsNavigation locale={locale} tab={tab} onTabChange={setTab} />
        <div className="member-docs-content">
          {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

          {tab === 'mcp' && <>
            <McpDocsBlock endpoint={routeState.endpoint} exposed={routeState.exposed} locale={locale} />
          </>}

          {tab === 'openapi' && <>
            <p className="member-admin-hint">{text.docsOpenapiDetail}</p>
            <p className="member-admin-hint"><code>{text.docsOpenapiBase}</code></p>
            <a className="member-docs-link" href={openapiUrl} target="_blank" rel="noreferrer">{text.docsOpenapiView}</a>
            {OPENAPI_GROUPS.map((group) => (
              <section className="member-docs-section" key={group.title}>
                <h2>{text[group.title]}</h2>
                <table className="member-mcp-docs-table member-docs-table">
                  <thead>
                    <tr><th>{text.docsOpenapiMethod}</th><th>{text.docsOpenapiPath}</th><th>{text.docsOpenapiDesc}</th><th>{text.docsOpenapiAuth}</th></tr>
                  </thead>
                  <tbody>
                    {group.endpoints.map((endpoint) => (
                      <tr key={`${endpoint.method}-${endpoint.path}`}>
                        <td><code>{endpoint.method}</code></td>
                        <td><code>{endpoint.path}</code></td>
                        <td>{text[endpoint.desc]}</td>
                        <td>{authLabel(endpoint.auth, text)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </section>
            ))}
          </>}

          {tab === 'faq' && <>
            <p className="member-admin-hint">{text.docsFaqDetail}</p>
            {FAQ_ITEMS.map((item) => (
              <details className="member-docs-faq" key={item.q}>
                <summary>{text[item.q]}</summary>
                <p>{text[item.a]}</p>
              </details>
            ))}
          </>}
        </div>
      </div>
    </div>
  );
}
