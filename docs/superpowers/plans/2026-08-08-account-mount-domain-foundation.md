# 账号—挂载领域基础 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立移除 Space 后可被数据库、授权守卫、REST、MCP 和 Web 契约共同复用的权限、挂载分类与内容源定位基础类型。

**Architecture:** 保留现有 Space 类型供尚未迁移的代码临时编译，但所有新代码只依赖新的 `ContentPermission`、挂载三维分类和 `contentref.Locator`。内容源定位在进入授权层前完成规范化；成员会话允许个人、共用挂载和协作三类来源，AI Token/MCP 只允许个人与共用挂载。

**Tech Stack:** Go 1.26、标准库、现有 `internal/storage` 相对路径规范化能力、Go `testing`。

**Working-tree rule:** 当前工作区包含未提交的跨挂载、上传、MCP 和 Web 改动。本阶段只修改本计划列出的新文件与 `internal/domain/types.go`；不修改现有 access/memberfiles/MCP/Web 文件，不提交 Git，除非用户另行明确要求。

---

### Task 1: 内容权限与挂载分类值对象

**Files:**
- Modify: `internal/domain/types.go`
- Create: `internal/domain/account_mount_test.go`

- [x] **Step 1: Write the failing tests**

在 `internal/domain/account_mount_test.go` 中覆盖：

```go
func TestContentPermissionAllows(t *testing.T) {
	tests := []struct {
		name     string
	have     ContentPermission
	required ContentPermission
	want     bool
	}{
		{name: "viewer reads", have: ContentPermissionViewer, required: ContentPermissionViewer, want: true},
		{name: "viewer cannot edit", have: ContentPermissionViewer, required: ContentPermissionEditor, want: false},
		{name: "editor reads", have: ContentPermissionEditor, required: ContentPermissionViewer, want: true},
		{name: "editor edits", have: ContentPermissionEditor, required: ContentPermissionEditor, want: true},
		{name: "unknown denied", have: ContentPermission("manager"), required: ContentPermissionViewer, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.have.Allows(tt.required); got != tt.want {
				t.Fatalf("%q.Allows(%q) = %v, want %v", tt.have, tt.required, got, tt.want)
			}
		})
	}
}

func TestMountClassificationValid(t *testing.T) {
	tests := []struct {
		name       string
	purpose    MountPurpose
	storage    StorageKind
	governance MountGovernance
	want       bool
	}{
		{name: "personal default", purpose: MountPurposePersonalDefault, storage: StorageKindManaged, governance: MountGovernanceSystem, want: true},
		{name: "normal common", purpose: MountPurposeCommon, storage: StorageKindExternal, governance: MountGovernanceNormal, want: true},
		{name: "restricted common", purpose: MountPurposeCommon, storage: StorageKindExternal, governance: MountGovernanceRestricted, want: true},
		{name: "managed common rejected", purpose: MountPurposeCommon, storage: StorageKindManaged, governance: MountGovernanceNormal, want: false},
		{name: "restricted personal rejected", purpose: MountPurposePersonalDefault, storage: StorageKindManaged, governance: MountGovernanceRestricted, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidMountClassification(tt.purpose, tt.storage, tt.governance); got != tt.want {
				t.Fatalf("ValidMountClassification(%q, %q, %q) = %v, want %v", tt.purpose, tt.storage, tt.governance, got, tt.want)
			}
		})
	}
}
```

- [x] **Step 2: Run the tests and verify RED**

Run:

```bash
GOCACHE=/private/tmp/omnora-account-mount-go-cache go test ./internal/domain
```

Expected: FAIL because `ContentPermission`, mount classification types, and their methods do not exist.

- [x] **Step 3: Implement the minimal domain types**

Add to `internal/domain/types.go`:

```go
type ContentPermission string

const (
	ContentPermissionNone   ContentPermission = ""
	ContentPermissionViewer ContentPermission = "viewer"
	ContentPermissionEditor ContentPermission = "editor"
)

func (p ContentPermission) Valid() bool {
	return p == ContentPermissionViewer || p == ContentPermissionEditor
}

func (p ContentPermission) Allows(required ContentPermission) bool {
	if !p.Valid() || !required.Valid() {
		return false
	}
	if p == ContentPermissionEditor {
		return true
	}
	return required == ContentPermissionViewer
}

type MountPurpose string
type StorageKind string
type MountGovernance string

const (
	MountPurposePersonalDefault MountPurpose = "personal_default"
	MountPurposeCommon          MountPurpose = "common"
	StorageKindManaged          StorageKind = "managed"
	StorageKindExternal         StorageKind = "external"
	MountGovernanceSystem       MountGovernance = "system"
	MountGovernanceNormal       MountGovernance = "normal"
	MountGovernanceRestricted   MountGovernance = "restricted"
)

func ValidMountClassification(purpose MountPurpose, storage StorageKind, governance MountGovernance) bool {
	switch purpose {
	case MountPurposePersonalDefault:
		return storage == StorageKindManaged && governance == MountGovernanceSystem
	case MountPurposeCommon:
		return storage == StorageKindExternal && (governance == MountGovernanceNormal || governance == MountGovernanceRestricted)
	default:
		return false
	}
}
```

- [x] **Step 4: Run the tests and verify GREEN**

Run:

```bash
GOCACHE=/private/tmp/omnora-account-mount-go-cache go test ./internal/domain
```

Expected: PASS.

- [x] **Step 5: Review the diff without committing**

Run:

```bash
git diff --check -- internal/domain/types.go internal/domain/account_mount_test.go
git diff -- internal/domain/types.go internal/domain/account_mount_test.go
```

Expected: no whitespace errors; diff contains only the new target-domain vocabulary and tests.

### Task 2: 不含 Space 的内容源定位联合类型

**Files:**
- Create: `internal/contentref/locator.go`
- Create: `internal/contentref/locator_test.go`

- [x] **Step 1: Write the failing tests**

在 `internal/contentref/locator_test.go` 中分别证明：个人源不能携带挂载/协作 ID；共用源必须携带 `mountId`；协作源必须携带 `collaborationId`；成员会话允许协作；自动化入口拒绝协作；所有路径都经过现有安全相对路径规范化。

```go
func TestNormalizeForAutomation(t *testing.T) {
	tests := []struct {
		name    string
		locator Locator
		want    Locator
		wantErr error
	}{
		{name: "personal", locator: Locator{Source: SourcePersonal, Path: "docs//notes"}, want: Locator{Source: SourcePersonal, Path: "docs/notes"}},
		{name: "common mount", locator: Locator{Source: SourceCommonMount, MountID: " mount-1 ", Path: "docs"}, want: Locator{Source: SourceCommonMount, MountID: "mount-1", Path: "docs"}},
		{name: "common requires mount", locator: Locator{Source: SourceCommonMount, Path: "docs"}, wantErr: ErrInvalidLocator},
		{name: "personal rejects mount", locator: Locator{Source: SourcePersonal, MountID: "mount-1", Path: "."}, wantErr: ErrInvalidLocator},
		{name: "path traversal rejected", locator: Locator{Source: SourcePersonal, Path: "docs/../notes"}, wantErr: ErrInvalidLocator},
		{name: "automation rejects collaboration", locator: Locator{Source: SourceCollaboration, CollaborationID: "collab-1", Path: "."}, wantErr: ErrCollaborationNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeForAutomation(tt.locator)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeForAutomation() error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("NormalizeForAutomation() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizeForMemberSessionAllowsCollaboration(t *testing.T) {
	got, err := NormalizeForMemberSession(Locator{Source: SourceCollaboration, CollaborationID: " collab-1 ", Path: "docs"})
	if err != nil {
		t.Fatalf("NormalizeForMemberSession() error = %v", err)
	}
	want := Locator{Source: SourceCollaboration, CollaborationID: "collab-1", Path: "docs"}
	if got != want {
		t.Fatalf("NormalizeForMemberSession() = %#v, want %#v", got, want)
	}
}
```

- [x] **Step 2: Run the tests and verify RED**

Run:

```bash
GOCACHE=/private/tmp/omnora-account-mount-go-cache go test ./internal/contentref
```

Expected: FAIL because the package and types do not exist.

- [x] **Step 3: Implement the minimal locator**

Create `internal/contentref/locator.go` with `SourcePersonal`, `SourceCommonMount`, `SourceCollaboration`, the three exact locator fields, and two normalization entry points. Both entry points trim identifiers, reject absolute路径、反斜杠和任意 `..` 路径组件，再调用 `storage.CleanRelativePath`；automation rejects collaboration before returning a normalized locator.

```go
type Source string

const (
	SourcePersonal      Source = "personal"
	SourceCommonMount   Source = "common_mount"
	SourceCollaboration Source = "collaboration"
)

type Locator struct {
	Source          Source `json:"source"`
	MountID        string `json:"mountId,omitempty"`
	CollaborationID string `json:"collaborationId,omitempty"`
	Path            string `json:"path"`
}

func NormalizeForMemberSession(locator Locator) (Locator, error) {
	return normalize(locator, true)
}

func NormalizeForAutomation(locator Locator) (Locator, error) {
	return normalize(locator, false)
}
```

`normalize` 必须使用互斥字段校验：

```go
switch locator.Source {
case SourcePersonal:
	valid = locator.MountID == "" && locator.CollaborationID == ""
case SourceCommonMount:
	valid = locator.MountID != "" && locator.CollaborationID == ""
case SourceCollaboration:
	if !allowCollaboration {
		return Locator{}, ErrCollaborationNotAllowed
	}
	valid = locator.MountID == "" && locator.CollaborationID != ""
default:
	return Locator{}, ErrUnsupportedSource
}
```

- [x] **Step 4: Run the package tests and verify GREEN**

Run:

```bash
GOCACHE=/private/tmp/omnora-account-mount-go-cache go test ./internal/contentref
```

Expected: PASS.

- [x] **Step 5: Run foundation regression tests**

Run:

```bash
GOCACHE=/private/tmp/omnora-account-mount-go-cache go test ./internal/domain ./internal/contentref ./internal/storage
```

Expected: PASS.

- [x] **Step 6: Review the diff without committing**

Run:

```bash
git diff --check -- internal/domain internal/contentref
git status --short -- internal/domain internal/contentref
```

Expected: only the four planned code/test files are listed; no existing dirty implementation file is touched.

## Self-review

- 规格覆盖：本计划只覆盖后续迁移共同依赖的权限两档、挂载三维分类、个人/共用/协作资源联合类型，以及 MCP/AI 排除协作；数据库、授权守卫、REST/MCP schema 和 Web 切换必须进入后续独立计划。
- 无占位：所有测试、类型、命令和预期结果均明确。
- 类型一致：`viewer/editor`、`personal_default/common`、`managed/external`、`system/normal/restricted`、`personal/common_mount/collaboration` 与权威规格一致。
- 工作区隔离：不改动当前已有未提交的 `memberfiles`、`mcpapi`、`server`、`transfer` 和 Web 文件。
