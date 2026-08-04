export type MemberLocale = 'zh-CN' | 'en-US';

export const localeStorageKey = 'omnora.member.locale';

export const localeMessages = {
  'zh-CN': {
    files: '文件',
    search: '搜索',
    transfers: '传输',
    shared: '共享',
    upload: '上传',
    newFolder: '新建文件夹',
    download: '下载',
    loading: '正在加载',
    signIn: '登录 Omnora',
    email: '邮箱或用户名',
    password: '密码',
    verificationCode: '验证码',
    signInAction: '登录',
    signOut: '退出登录',
    myFiles: '我的文件',
    recent: '最近使用',
    sharedWithMe: '共享给我',
    recycleBin: '回收站',
    spaces: '空间',
    mounts: '存储位置',
    readOnly: '只读',
    readWrite: '可读写',
    name: '名称',
    size: '大小',
    modified: '修改时间',
    actions: '操作',
    list: '列表',
    grid: '网格',
    open: '打开',
    cancel: '取消',
    create: '创建',
    folderName: '文件夹名称',
    emptyFolder: '这个文件夹为空',
    noMount: '当前空间没有可用的存储位置',
    noSpaces: '当前账户没有可访问的空间',
    searchPlaceholder: '搜索当前空间中的文件',
    searchScope: '搜索仅包含已完成索引的存储位置',
    clearSearch: '返回文件',
	clearCompleted: '清除已完成',
	loadMore: '加载更多',
	resumeUploadHint: '刷新后请重新选择同一个文件以继续上传',
    uploadBlocked: '当前存储位置为只读，不能上传或新建文件夹',
    uploadProgress: '正在上传',
    uploadComplete: '上传完成',
    uploadFailed: '上传失败',
    retryWithSameFile: '重新选择同一文件可继续未完成上传',
    language: '语言',
    account: '账户',
    sessionExpired: '登录状态已失效，请重新登录',
    refresh: '刷新',
    error: '操作失败',
    selectFile: '选择文件',
    activeTransfers: '传输任务',
  },
  'en-US': {
    files: 'Files',
    search: 'Search',
    transfers: 'Transfers',
    shared: 'Shared',
    upload: 'Upload',
    newFolder: 'New folder',
    download: 'Download',
    loading: 'Loading',
    signIn: 'Sign in to Omnora',
    email: 'Email or username',
    password: 'Password',
    verificationCode: 'Verification code',
    signInAction: 'Sign in',
    signOut: 'Sign out',
    myFiles: 'My files',
    recent: 'Recent',
    sharedWithMe: 'Shared with me',
    recycleBin: 'Recycle bin',
    spaces: 'Spaces',
    mounts: 'Storage locations',
    readOnly: 'Read only',
    readWrite: 'Read and write',
    name: 'Name',
    size: 'Size',
    modified: 'Modified',
    actions: 'Actions',
    list: 'List',
    grid: 'Grid',
    open: 'Open',
    cancel: 'Cancel',
    create: 'Create',
    folderName: 'Folder name',
    emptyFolder: 'This folder is empty',
    noMount: 'No storage location is available in this space',
    noSpaces: 'This account cannot access any spaces',
    searchPlaceholder: 'Search files in this space',
    searchScope: 'Search includes indexed storage locations only',
    clearSearch: 'Back to files',
	clearCompleted: 'Clear completed',
	loadMore: 'Load more',
	resumeUploadHint: 'After refreshing, select the same file to resume the upload',
    uploadBlocked: 'This storage location is read only',
    uploadProgress: 'Uploading',
    uploadComplete: 'Upload complete',
    uploadFailed: 'Upload failed',
    retryWithSameFile: 'Select the same file again to resume the upload',
    language: 'Language',
    account: 'Account',
    sessionExpired: 'Your session has expired. Sign in again.',
    refresh: 'Refresh',
    error: 'Operation failed',
    selectFile: 'Choose files',
    activeTransfers: 'Transfers',
  },
} as const;

export function resolveLocale(value: string | null | undefined): MemberLocale {
  return value === 'en-US' ? 'en-US' : 'zh-CN';
}

export function getStoredLocale(): MemberLocale {
  try {
    return resolveLocale(globalThis.localStorage?.getItem(localeStorageKey));
  } catch {
    return 'zh-CN';
  }
}

export function saveLocale(locale: MemberLocale) {
  try {
    globalThis.localStorage?.setItem(localeStorageKey, locale);
  } catch {
    // Storage can be unavailable in a private or embedded browser context.
  }
}
