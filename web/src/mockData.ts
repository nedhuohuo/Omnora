import type { AdminRisk, AiToken, AppBootstrap, AuditRow, FileItem, Mount, RouteGroup, ShareItem, Tone, Transfer } from './types';

export const routeGroups: RouteGroup[] = [
  { id: 'member_web', label: 'Member Web', exposed: true, entry: 'both', risk: 'LAN + trusted proxy', tone: 'ok' },
  { id: 'admin_web', label: 'Admin Web', exposed: true, entry: 'lan_http', risk: 'not public', tone: 'warn' },
  { id: 'share_web', label: 'Share', exposed: true, entry: 'proxy_https', risk: 'fragment-only secret', tone: 'ok' },
  { id: 'rest', label: 'REST', exposed: false, entry: 'disabled', risk: 'closed', tone: 'muted' },
  { id: 'mcp', label: 'MCP', exposed: false, entry: 'disabled', risk: 'closed', tone: 'muted' },
  { id: 'openapi', label: 'OpenAPI', exposed: false, entry: 'disabled', risk: 'closed', tone: 'muted' },
];

export const mounts: Mount[] = [
  { name: 'HomeVault', space: 'Li Personal', mode: 'read-write', index: 'running', health: 'normal', tone: 'ok' },
  { name: 'CameraArchive', space: 'Family', mode: 'read-only', index: 'paused', health: 'readonly', tone: 'warn' },
  { name: 'SharedProjects', space: 'Studio', mode: 'read-write', index: 'first scan 72%', health: 'normal', tone: 'info' },
  { name: 'OldDrive', space: 'Family', mode: 'read-only', index: 'excluded', health: 'identity drift', tone: 'danger' },
];

export const files: FileItem[] = [
  {
    id: 'f-001',
    name: '2026 Family Receipts',
    kind: 'folder',
    size: '128 items',
    modified: 'Today 09:42',
    mount: 'HomeVault',
    access: 'read-write',
    status: 'available',
    statusTone: 'ok',
    index: 'included',
    selected: true,
  },
  {
    id: 'f-002',
    name: 'landing-notes.md',
    kind: 'markdown',
    size: '42 KB',
    modified: 'Yesterday 21:13',
    mount: 'SharedProjects',
    access: 'read-write',
    status: 'recheck before preview',
    statusTone: 'info',
    index: 'pending scan',
  },
  {
    id: 'f-003',
    name: 'garden-plan.pdf',
    kind: 'pdf',
    size: '8.4 MB',
    modified: 'Jul 30, 2026',
    mount: 'HomeVault',
    access: 'read-write',
    status: 'previewable',
    statusTone: 'ok',
    index: 'included',
  },
  {
    id: 'f-004',
    name: 'tax-export.xlsx',
    kind: 'office',
    size: '1.2 MB',
    modified: 'Jul 29, 2026',
    mount: 'HomeVault',
    access: 'read-write',
    status: 'download only',
    statusTone: 'warn',
    index: 'included',
  },
  {
    id: 'f-005',
    name: 'summer-video.mov',
    kind: 'audio',
    size: '624 MB',
    modified: 'Jul 27, 2026',
    mount: 'CameraArchive',
    access: 'read-only',
    status: 'native media only',
    statusTone: 'info',
    index: 'excluded',
  },
  {
    id: 'f-006',
    name: 'archive-restore.zip',
    kind: 'archive',
    size: '2.1 GB',
    modified: 'Jul 21, 2026',
    mount: 'OldDrive',
    access: 'read-only',
    status: 'mount drift blocked',
    statusTone: 'danger',
    index: 'excluded',
  },
];

export const transfers: Transfer[] = [
  { name: 'Photos / 2026 / raw', operation: 'directory upload', progress: 64, state: 'paused, waiting for network', tone: 'warn' },
  { name: 'studio-export.mov', operation: 'range download', progress: 38, state: 'downloading', tone: 'info' },
  { name: 'Family -> Studio / invoices', operation: 'cross-mount copy', progress: 91, state: 'partial complete, retry available', tone: 'warn' },
];

export const adminRisks: AdminRisk[] = [
  { label: 'Public admin', value: 'off', tone: 'ok' },
  { label: 'LAN encryption', value: 'HTTP warning', tone: 'warn' },
  { label: 'Long token', value: '2 over 90d', tone: 'warn' },
  { label: 'Backup age', value: '31h', tone: 'info' },
  { label: 'Failed mount', value: 'OldDrive drift', tone: 'danger' },
];

export const auditRows: AuditRow[] = [
  { time: '09:54', actor: 'admin@local', event: 'route_group_update', target: 'mcp disabled', result: 'success' },
  { time: '09:35', actor: 'li@local', event: 'share_ticket_exchange', target: 'share pub_8K2', result: 'success, secret redacted' },
  { time: '09:08', actor: 'system', event: 'mount_revalidate', target: 'OldDrive', result: 'mount_identity_unverifiable' },
  { time: '08:44', actor: 'chen@local', event: 'token_use', target: 'tok_ai_docs', result: 'scope_no_longer_valid' },
];

export const shares: ShareItem[] = [
  { id: 'pub_8K2', target: 'garden-plan.pdf', capability: 'preview + download', expires: '6 days', use: '2 / 20 visits', status: 'active', tone: 'ok' },
  { id: 'pub_Q19', target: 'Family / CameraArchive', capability: 'preview only', expires: '14 hours', use: '9 / 10 visits', status: 'active', tone: 'warn' },
  { id: 'pub_R04', target: 'archive-restore.zip', capability: 'download', expires: 'expired', use: '12 / 12 visits', status: 'exhausted', tone: 'danger' },
];

export const tokens: AiToken[] = [
  { id: 'tok_ai_docs', name: 'Local editor MCP', scope: 'read metadata, read text, upload disabled', boundary: 'Studio / SharedProjects / docs', status: 'scope_no_longer_valid', tone: 'danger' },
  { id: 'tok_photos', name: 'Photo sorter REST', scope: 'list, metadata', boundary: 'Family / CameraArchive', status: 'active', tone: 'ok' },
  { id: 'tok_lab', name: 'NAS lab uploader', scope: 'upload enabled, no overwrite', boundary: 'Li Personal / HomeVault / inbox', status: 'plaintext_shown_once', tone: 'warn' },
];

export const mockBootstrap: AppBootstrap = {
  routeGroups,
  mounts,
  files,
  transfers,
  adminRisks,
  auditRows,
  shares,
  tokens,
};
