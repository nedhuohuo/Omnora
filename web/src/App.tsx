import { BrowserRouter, Link, Navigate, Route, Routes, useNavigate, useParams } from 'react-router-dom';
import MemberFilesApp from './member/MemberFilesApp';
import SharePortalApp from './SharePortalApp';
import {
  isAdminTab,
  isMemberTab,
  workspacePath,
  workspaceRoutePatterns,
  type WorkspaceAdminTab,
  type WorkspaceDestination,
} from './workspaceRoutes';

function RouteUnavailable({ title, detail }: { title: string; detail: string }) {
  return <main className="route-unavailable"><strong>Omnora</strong><h1>{title}</h1><p>{detail}</p><Link to="/app/files">返回文件空间</Link></main>;
}

function WorkspaceRoute({ workspace, legacyAdmin = false }: { workspace: 'member' | 'admin'; legacyAdmin?: boolean }) {
  const navigate = useNavigate();
  const { tab, resourceId } = useParams<{ tab?: string; resourceId?: string }>();
  const destination: WorkspaceDestination = workspace === 'admin'
    ? { workspace: 'admin', tab: (isAdminTab(tab) ? tab : 'overview') as WorkspaceAdminTab, resourceId }
    : { workspace: 'member', tab: isMemberTab(tab) ? tab : 'files' };

  if (!legacyAdmin && workspace === 'admin' && tab && !isAdminTab(tab)) {
    return <Navigate to="/app/admin/overview" replace />;
  }
  if (workspace === 'member' && tab && !isMemberTab(tab)) {
    return <Navigate to="/app/files" replace />;
  }

  return (
    <MemberFilesApp
      entry={workspace}
      route={destination}
      legacyAdminPath={legacyAdmin}
      onNavigate={(next, replace = false) => navigate(workspacePath(next), { replace })}
    />
  );
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/app/files" replace />} />
      <Route path="/app" element={<Navigate to="/app/files" replace />} />
      <Route path={workspaceRoutePatterns.canonicalAdmin} element={<WorkspaceRoute workspace="admin" />} />
      <Route path={workspaceRoutePatterns.canonicalMember} element={<WorkspaceRoute workspace="member" />} />
      <Route path={workspaceRoutePatterns.legacyAdmin} element={<WorkspaceRoute workspace="admin" legacyAdmin />} />
      <Route path="/share/*" element={<SharePortalApp />} />
      <Route path="*" element={<RouteUnavailable title="页面不存在" detail="请返回成员文件空间。" />} />
    </Routes>
  );
}

export default function App() {
  return <BrowserRouter><AppRoutes /></BrowserRouter>;
}
