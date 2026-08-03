export type Tone = 'ok' | 'info' | 'warn' | 'danger' | 'muted';

export type FileItem = {
  id: string;
  name: string;
  kind: 'folder' | 'image' | 'pdf' | 'markdown' | 'office' | 'audio' | 'archive';
  size: string;
  modified: string;
  mount: string;
  access: 'read-write' | 'read-only';
  status: string;
  statusTone: Tone;
  index: string;
  selected?: boolean;
};

export type Mount = {
  id?: string;
  name: string;
  space: string;
  mode: 'read-write' | 'read-only';
  index: string;
  health: string;
  tone: Tone;
};

export type Transfer = {
  name: string;
  operation: string;
  progress: number;
  state: string;
  tone: Tone;
};

export type RouteGroup = {
  id: string;
  label: string;
  exposed: boolean;
  entry: 'lan_http' | 'proxy_https' | 'both' | 'disabled';
  risk: string;
  tone: Tone;
};

export type AdminRisk = {
  label: string;
  value: string;
  tone: Tone;
};

export type AuditRow = {
  time: string;
  actor: string;
  event: string;
  target: string;
  result: string;
};

export type ShareItem = {
  id: string;
  target: string;
  capability: string;
  expires: string;
  use: string;
  status: string;
  tone: Tone;
};

export type AiToken = {
  id: string;
  name: string;
  scope: string;
  boundary: string;
  status: string;
  tone: Tone;
};

export type AppBootstrap = {
  routeGroups: RouteGroup[];
  mounts: Mount[];
  files: FileItem[];
  transfers: Transfer[];
  adminRisks: AdminRisk[];
  auditRows: AuditRow[];
  shares: ShareItem[];
  tokens: AiToken[];
};
