import { useState, useEffect, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import type { TreeNode, FileCommit, Dataset } from '../types'
import * as api from '../api'

export default function WorkspacePage() {
  const { folderId } = useParams()
  const navigate = useNavigate()
  const [dataset, setDataset] = useState<Dataset | null>(null)
  const [rootTree, setRootTree] = useState<TreeNode | null>(null)
  const [curFolder, setCurFolder] = useState<TreeNode | null>(null)
  const [selFile, setSelFile] = useState<TreeNode | null>(null)
  const [content, setContent] = useState('')
  const [origContent, setOrigContent] = useState('')
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState('')
  const [commits, setCommits] = useState<FileCommit[]>([])
  const [showCommits, setShowCommits] = useState(false)
  const [showNewFile, setShowNewFile] = useState(false)
  const [showNewFolder, setShowNewFolder] = useState(false)
  const [showCommitDlg, setShowCommitDlg] = useState(false)
  const [newFileName, setNewFileName] = useState('')
  const [newFolderName, setNewFolderName] = useState('')
  const [commitMsg, setCommitMsg] = useState('')
  const [renamingWs, setRenamingWs] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [deleteName, setDeleteName] = useState('')
  const [deleteMsg, setDeleteMsg] = useState('')
  const [renameValue, setRenameValue] = useState('')
  const initialized = useRef(false)

  useEffect(() => {
    if (initialized.current) return
    initialized.current = true
    init()
  }, [])

  async function init() {
    try {
      let ds = await api.listDatasets()
      if (!ds || ds.length === 0) ds = [await api.createDataset('llmwiki')]
      setDataset(ds[0])
      const tree = await api.getFolderTree(ds[0].id)
      setRootTree(tree)
      // Navigate to the folder from URL, or first folder
      const targetFolderId = folderId && folderId !== 'default' ? folderId : tree?.children?.find(c => c.type === 'folder')?.id
      if (targetFolderId) {
        const node = findNode(tree, targetFolderId)
        if (node) selectFolder(node)
      }
    } catch (e: any) { setError('Failed to init: ' + e.message) }
  }

  // Refresh the full tree and update curFolder to the new node.
  async function refreshCurFolder() {
    if (!dataset || !curFolder) return
    const tree = await api.getFolderTree(dataset.id)
    setRootTree(tree)
    const fresh = findNode(tree, curFolder.id)
    if (fresh) setCurFolder(fresh)
  }

  function selectFolder(f: TreeNode) {
    setCurFolder(f); setSelFile(null); setDirty(false); setError('')
    setShowCommits(false)
    navigate(`/ws/${f.id}`, { replace: true })
  }

  async function selectFile(f: TreeNode) {
    setSelFile(f)
    try {
      const text = await api.getFileContent(f.id)
      setContent(text); setOrigContent(text); setDirty(false)
    } catch { setContent(''); setOrigContent('') }
  }

  function viewCommit(c: FileCommit) {
    navigate(`/ws/${curFolder?.id}/commit/${c.id}`)
  }

  async function handleCreateFile() {
    if (!newFileName.trim() || !curFolder || !dataset) return
    try {
      await api.createTextFile(curFolder.id, newFileName.trim(), `# ${newFileName.trim()}\n\n`)
      setShowNewFile(false); setNewFileName('')
      await refreshCurFolder()
    } catch (e: any) { setError('Create failed: ' + e.message) }
  }

  async function handleCreateFolder() {
    if (!newFolderName.trim() || !curFolder || !dataset) return
    try {
      await api.createFolder(curFolder.id, newFolderName.trim())
      setShowNewFolder(false); setNewFolderName('')
      await refreshCurFolder()
    } catch (e: any) { setError('Create failed: ' + e.message) }
  }

  async function handleCommit() {
    if (!commitMsg.trim() || !selFile || !dirty || !dataset || !curFolder) return
    try {
      await api.createCommit(curFolder.id, commitMsg, [
        { file_id: selFile.id, file_name: selFile.name, operation: 'modify', content }
      ])
      setShowCommitDlg(false); setCommitMsg(''); setDirty(false); setOrigContent(content)
      setCommits(await api.listFolderCommits(curFolder.id))
    } catch (e: any) { setError('Commit failed: ' + e.message) }
  }

  function deleteItem(fileID: string, name: string) {
    setDeleteTarget(fileID)
    setDeleteName(name)
    setDeleteMsg('')
  }

  async function confirmDelete() {
    if (!deleteTarget || !curFolder || !dataset) return
    try {
      await api.createCommit(curFolder.id, deleteMsg || `Delete ${deleteName}`, [
        { file_id: deleteTarget, file_name: deleteName, operation: 'delete', content: '' }
      ])
      setDeleteTarget(null)
      if (selFile?.id === deleteTarget) { setSelFile(null); setContent('') }
      await refreshCurFolder()
      setCommits(await api.listFolderCommits(curFolder.id))
    } catch (e: any) { setError('Delete failed: ' + e.message); setDeleteTarget(null) }
  }

  async function loadCommits() {
    if (!curFolder) return
    setShowCommits(!showCommits)
    if (!showCommits) api.listFolderCommits(curFolder.id).then(setCommits).catch(() => {})
  }

  async function startRename(f: TreeNode) {
    setRenamingWs(f.id); setRenameValue(f.name)
  }

  async function confirmRename() {
    if (!renamingWs || !renameValue.trim()) { setRenamingWs(null); return }
    try {
      await api.renameFile(renamingWs, renameValue.trim())
      setRenamingWs(null)
      if (dataset) setRootTree(await api.getFolderTree(dataset.id))
    } catch (e: any) { setError('Rename failed: ' + e.message); setRenamingWs(null) }
  }

  async function ensureLevel1Folder() {
    if (!dataset || !rootTree) return
    await api.createFolder(rootTree.id, 'My Workspace')
    const tree = await api.getFolderTree(dataset.id)
    setRootTree(tree)
    const l1 = tree?.children?.find(c => c.type === 'folder')
    if (l1) selectFolder(l1)
  }

  const folders = rootTree?.children?.filter(c => c.type === 'folder') || []
  const files = curFolder?.children?.filter(c => c.type === 'file') || []
  const subfolders = curFolder?.children?.filter(c => c.type === 'folder') || []

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-hdr">
          <span className="logo">llmwiki</span>
          <span className="badge">EDIT</span>
        </div>

        <div className="section-hdr" style={{padding:'6px 20px'}}>
          <span className="workspace-title" style={{padding:0,textTransform:'uppercase',letterSpacing:'1px'}}>Workspace</span>
          <button className="btn-icon-sm" onClick={ensureLevel1Folder} title="New Workspace">+</button>
        </div>
        {folders.map(f => (
          <div key={f.id}
            className={`nav-item ${curFolder?.id === f.id ? 'active' : ''}`}
            onDoubleClick={() => startRename(f)}>
            {renamingWs === f.id ? (
              <input className="rename-input"
                value={renameValue} onChange={e => setRenameValue(e.target.value)}
                onBlur={confirmRename}
                onKeyDown={e => { if (e.key === 'Enter') confirmRename(); if (e.key === 'Escape') setRenamingWs(null) }}
                onClick={e => e.stopPropagation()} autoFocus />
            ) : (
              <span className="nav-item-content" onClick={() => selectFolder(f)}>
                <span className="nav-icon">📁</span>
                <span className="nav-label">{f.name}</span>
                <span className="nav-arrow">{'>'}</span>
              </span>
            )}
          </div>
        ))}

        {curFolder && (
          <>
            <div className="divider" />
            <div className="section-hdr">
              <span className="section-label">{curFolder.name}</span>
              <div className="section-actions">
                <button className="btn-icon-sm" onClick={() => setShowNewFile(true)} title="New File">+</button>
                <button className="btn-icon-sm" onClick={() => setShowNewFolder(true)} title="New Folder">📁</button>
                <button className="btn-icon-sm" onClick={loadCommits} title="History">🕐</button>
              </div>
            </div>
            <div className="files-section">
              {subfolders.map(f => (
                <div key={f.id} className="nav-item sub folder" onClick={() => selectFolder(f)}>
                  <span className="nav-icon">📁</span>
                  <span className="nav-label">{f.name}</span>
                  <span className="delete-btn" onClick={e => { e.stopPropagation(); deleteItem(f.id, f.name); }}>✕</span>
                </div>
              ))}
              {files.map(f => (
                <div key={f.id} className={`nav-item sub file ${selFile?.id === f.id ? 'active' : ''}`} onClick={() => selectFile(f)}>
                  <span className="nav-icon">📄</span>
                  <span className="nav-label">{f.name}</span>
                  <span className="delete-btn" onClick={e => { e.stopPropagation(); deleteItem(f.id, f.name); }}>✕</span>
                </div>
              ))}
            </div>
          </>
        )}

        {showCommits && (
          <div className="commit-panel">
            <div className="commit-hdr">
              <span>History</span>
              <button className="btn-icon-sm" onClick={() => setShowCommits(false)}>✕</button>
            </div>
            <div className="commit-list">
              {commits.length === 0 && <div className="hint">No commits yet</div>}
              {commits.map(c => (
                <div key={c.id} className="commit-item" onClick={() => viewCommit(c)}>
                  <div className="commit-msg">{c.message || '(no message)'}</div>
                  <div className="commit-meta">{c.file_count} files · {fmtTime(c.create_time)}</div>
                </div>
              ))}
            </div>
          </div>
        )}
      </aside>

      <main className="main">
        {error && <div className="error-bar">{error}</div>}

        {!curFolder && (
          <div className="welcome">
            <h1>llmwiki</h1>
            <p>A version-controlled wiki powered by Markdown</p>
            {folders.length === 0 ? (
              <>
                <p className="hint" style={{marginTop:8}}>No workspace folders yet</p>
                <button className="btn-primary" style={{marginTop:16}} onClick={ensureLevel1Folder}>+ Create First Workspace</button>
              </>
            ) : (
              <p className="hint">Select a workspace folder from the sidebar</p>
            )}
          </div>
        )}

        {curFolder && !selFile && (
          <div className="folder-view">
            <div className="folder-hdr">
              <div>
                <h2>{curFolder.name}</h2>
                <p className="folder-path">{curFolder.id?.slice(0,8)}... · {files.length + subfolders.length} items</p>
              </div>
              <button className="btn-primary" onClick={() => setShowNewFile(true)}>
                <span className="btn-icon-text">+</span> New Page
              </button>
            </div>
            <div className="file-grid-title">Pages</div>
            <div className="file-grid">
              {files.map(f => (
                <div key={f.id} className="file-card" onClick={() => selectFile(f)}>
                  <div className="card-preview"><span className="card-icon">📄</span></div>
                  <div className="card-info"><span className="card-name">{f.name}</span><span className="card-size">{formatSize(f.size)}</span></div>
                </div>
              ))}
            </div>
            {subfolders.length > 0 && (
              <>
                <div className="file-grid-title" style={{marginTop:24}}>Folders</div>
                <div className="file-grid">
                  {subfolders.map(f => (
                    <div key={f.id} className="file-card folder-card" onClick={() => selectFolder(f)}>
                      <div className="card-preview"><span className="card-icon">📁</span></div>
                      <div className="card-info"><span className="card-name">{f.name}</span></div>
                    </div>
                  ))}
                </div>
              </>
            )}
            {files.length === 0 && subfolders.length === 0 && (
              <div className="empty-state"><p>This folder is empty</p><button className="btn-primary" onClick={() => setShowNewFile(true)}>Create your first page</button></div>
            )}
          </div>
        )}

        {selFile && (
          <div className="editor-view">
            <div className="editor-toolbar">
              <div className="editor-toolbar-left">
                <span className="editor-breadcrumb">{curFolder?.name} / <strong>{selFile.name}</strong></span>
              </div>
              <div className="toolbar-actions">
                {dirty && <button className="btn-primary" onClick={() => setShowCommitDlg(true)}>Commit Changes</button>}
              </div>
            </div>
            <div className="editor-body">
              <textarea className="editor-textarea" value={content}
                onChange={e => { setContent(e.target.value); setDirty(e.target.value !== origContent) }}
                placeholder="Start writing in Markdown..." spellCheck={false} />
              <div className="preview-pane">
                <div className="preview-hdr">Preview</div>
                <div className="preview-content" dangerouslySetInnerHTML={{ __html: renderMarkdown(content) }} />
              </div>
            </div>
            {dirty && (
              <div className="editor-footer">
                <span className="footer-dirty">● Unsaved changes</span>
                <button className="btn-primary" onClick={() => setShowCommitDlg(true)}>Commit Changes</button>
              </div>
            )}
          </div>
        )}
      </main>

      {/* Dialogs */}
      {showNewFile && (
        <div className="overlay" onClick={() => setShowNewFile(false)}>
          <div className="dialog" onClick={e => e.stopPropagation()}>
            <h3>Create Page</h3>
            <input className="input" value={newFileName} onChange={e => setNewFileName(e.target.value)}
              placeholder="e.g. getting-started.md" autoFocus onKeyDown={e => e.key === 'Enter' && handleCreateFile()} />
            <div className="dlg-actions">
              <button className="btn" onClick={() => setShowNewFile(false)}>Cancel</button>
              <button className="btn-primary" onClick={handleCreateFile} disabled={!newFileName.trim()}>Create</button>
            </div>
          </div>
        </div>
      )}

      {showNewFolder && (
        <div className="overlay" onClick={() => setShowNewFolder(false)}>
          <div className="dialog" onClick={e => e.stopPropagation()}>
            <h3>Create Folder</h3>
            <input className="input" value={newFolderName} onChange={e => setNewFolderName(e.target.value)}
              placeholder="Folder name" autoFocus onKeyDown={e => e.key === 'Enter' && handleCreateFolder()} />
            <div className="dlg-actions">
              <button className="btn" onClick={() => setShowNewFolder(false)}>Cancel</button>
              <button className="btn-primary" onClick={handleCreateFolder} disabled={!newFolderName.trim()}>Create</button>
            </div>
          </div>
        </div>
      )}

      {showCommitDlg && (
        <div className="overlay" onClick={() => setShowCommitDlg(false)}>
          <div className="dialog dialog-commit" onClick={e => e.stopPropagation()}>
            <h3>Commit Changes</h3>
            <div className="commit-summary">
              <div className="commit-file-row">
                <span>📄 {selFile?.name}</span>
                <span className="badge badge-modify">modified</span>
              </div>
            </div>
            <input className="input" value={commitMsg} onChange={e => setCommitMsg(e.target.value)}
              placeholder="Describe your changes..." autoFocus onKeyDown={e => e.key === 'Enter' && handleCommit()} />
            <div className="dlg-actions">
              <button className="btn" onClick={() => setShowCommitDlg(false)}>Cancel</button>
              <button className="btn-primary" onClick={handleCommit} disabled={!commitMsg.trim()}>Commit</button>
            </div>
          </div>
        </div>
      )}

      {/* Delete commit dialog */}
      {deleteTarget && (
        <div className="overlay" onClick={() => setDeleteTarget(null)}>
          <div className="dialog" onClick={e => e.stopPropagation()}>
            <h3>🗑️ Delete & Commit</h3>
            <p className="hint" style={{marginBottom:12}}>Deleting <strong>{deleteName}</strong> will create a commit record</p>
            <input className="input" value={deleteMsg}
              onChange={e => setDeleteMsg(e.target.value)}
              placeholder={`Delete ${deleteName}`} autoFocus
              onKeyDown={e => e.key === 'Enter' && confirmDelete()} />
            <div className="dlg-actions">
              <button className="btn" onClick={() => setDeleteTarget(null)}>Cancel</button>
              <button className="btn-primary" onClick={confirmDelete}>Delete & Commit</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function findNode(tree: TreeNode, id: string): TreeNode | undefined {
  if (tree.id === id) return tree
  for (const child of tree.children || []) {
    const found = findNode(child, id)
    if (found) return found
  }
}

function renderMarkdown(md: string): string {
  return md
    .replace(/^### (.+)$/gm, '<h3>$1</h3>')
    .replace(/^## (.+)$/gm, '<h2>$1</h2>')
    .replace(/^# (.+)$/gm, '<h1>$1</h1>')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/\*(.+?)\*/g, '<em>$1</em>')
    .replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code class="lang-$1">$2</code></pre>')
    .replace(/`(.+?)`/g, '<code>$1</code>')
    .replace(/^- (.+)$/gm, '<li class="bullet">$1</li>')
    .replace(/^(\d+)\. (.+)$/gm, '<li class="num">$2</li>')
    .replace(/!\[(.*?)\]\((.*?)\)/g, '<img alt="$1" src="$2" />')
    .replace(/\[(.*?)\]\((.*?)\)/g, '<a href="$2">$1</a>')
    .replace(/\n\n/g, '</p><p>')
    .replace(/\n/g, '<br/>')
}

function fmtTime(ts?: number): string {
  return ts ? new Date(ts).toLocaleString('zh-CN') : ''
}

function formatSize(bytes?: number): string {
  if (!bytes) return ''
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}
