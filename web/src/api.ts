import type { TreeNode, FileCommit, Dataset, DocsFile } from './types'

const BASE = '/api/v1'

async function api<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + url, {
    headers: { 'Content-Type': 'application/json', ...init?.headers },
    ...init,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || res.statusText)
  }
  const json = await res.json()
  return json.data ?? json
}

export async function listDatasets(): Promise<Dataset[]> {
  const r = await api<{ data: Dataset[] }>('/datasets')
  return Array.isArray(r) ? r : (r as any).data || []
}

export async function createDataset(name: string): Promise<Dataset> {
  return api<Dataset>('/datasets', { method: 'POST', body: JSON.stringify({ name, embd_id: 'default' }) })
}

export async function getWorkspaceChanges(folderID: string): Promise<{ file_id: string; file_name: string; operation: string }[]> {
  const r = await api<{ data: any[] }>(`/workspaces/${folderID}/changes`)
  return Array.isArray(r) ? r : (r as any).data || []
}

export async function getFolderTree(folderID: string): Promise<TreeNode> {
  return api<TreeNode>(`/folders/${folderID}/tree`)
}

export async function getFileContent(fileID: string): Promise<string> {
  const res = await fetch(BASE + `/files/${fileID}`)
  return res.text()
}

export async function createTextFile(parentID: string, name: string, content: string): Promise<DocsFile> {
  return api<DocsFile>('/files/text', {
    method: 'POST',
    body: JSON.stringify({ parent_id: parentID, name, content }),
  })
}

export async function createFolder(parentID: string, name: string): Promise<DocsFile> {
  return api<DocsFile>('/files/folder', {
    method: 'POST',
    body: JSON.stringify({ parent_id: parentID, name }),
  })
}

export async function deleteWorkspace(folderID: string): Promise<void> {
  await api(`/workspaces/${folderID}`, { method: 'DELETE' })
}

export async function deleteFiles(fileIDs: string[]): Promise<void> {
  await api('/files', { method: 'DELETE', body: JSON.stringify({ file_ids: fileIDs }) })
}

export async function renameFile(fileID: string, newName: string): Promise<void> {
  await api('/files/move', {
    method: 'POST',
    body: JSON.stringify({ file_ids: [fileID], dest_parent_id: '', new_names: { [fileID]: newName } }),
  })
}

export async function listFolderCommits(folderID: string, page = 1): Promise<FileCommit[]> {
  const r = await api<{ data: FileCommit[] }>(`/workspaces/${folderID}/commits?page=${page}&page_size=50`)
  return Array.isArray(r) ? r : (r as any).data || []
}

export async function getCommitTree(commitID: string): Promise<TreeNode> {
  return api<TreeNode>(`/commits/${commitID}/tree`)
}

export async function getCommitFileContent(commitID: string, fileID: string): Promise<string> {
  const res = await fetch(BASE + `/commits/${commitID}/files/${fileID}/content`)
  return res.text()
}

export async function createCommit(
  folderID: string,
  message: string,
  files: { file_id: string; file_name: string; operation: string; content: string }[]
): Promise<FileCommit> {
  return api<FileCommit>(`/workspaces/${folderID}/commits`, {
    method: 'POST',
    body: JSON.stringify({ message, files }),
  })
}
