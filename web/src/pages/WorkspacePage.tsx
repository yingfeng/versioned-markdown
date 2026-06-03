import { useState, useEffect, useRef, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import type { TreeNode, FileCommit, Dataset } from '../types'
import * as api from '../api'
import MarkdownEditor from '../components/MarkdownEditor'
import GraphView from '../components/GraphView'
import { useTheme } from '../lib/theme/ThemeContext'
import { buildGraph, type GraphNode, type GraphEdge } from '../lib/graph/wiki-graph'

export default function WorkspacePage() {
  const { name } = useParams()
  const navigate = useNavigate()
  const { theme, toggleTheme } = useTheme()
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
  const [deleteWsId, setDeleteWsId] = useState<string | null>(null)
  const [showCommitAll, setShowCommitAll] = useState(false)
  const [commitAllChanges, setCommitAllChanges] = useState<{ file_id: string; file_name: string; operation: string }[]>([])
  const [commitAllMsg, setCommitAllMsg] = useState('')
  const [renameValue, setRenameValue] = useState('')
  const [showGraph, setShowGraph] = useState(false)
  const [graphNodes, setGraphNodes] = useState<GraphNode[]>([])
  const [graphEdges, setGraphEdges] = useState<GraphEdge[]>([])
  const [graphLoading, setGraphLoading] = useState(false)
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set())
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
      // Resolve name → folder: try name match first, fallback to ID
      let targetNode = tree?.children?.find(c => c.type === 'folder' && c.name === decodeURIComponent(name || ''))
      if (!targetNode && name && name !== 'default') targetNode = findNode(tree, name)
      if (!targetNode) targetNode = tree?.children?.find(c => c.type === 'folder')
      if (targetNode) selectFolder(targetNode)
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
    navigate(`/${encodeURIComponent(f.name)}`, { replace: true })
  }

  async function selectFile(f: TreeNode) {
    setShowGraph(false)
    try {
      const text = await api.getFileContent(f.id)
      setContent(text); setOrigContent(text); setDirty(false)
      setSelFile(f)
    } catch { setContent(''); setOrigContent(''); setSelFile(f) }
  }

  function viewCommit(c: FileCommit) {
    navigate(`/${encodeURIComponent(curFolder?.name || '')}/commits/${c.id}`)
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

  async function deleteWorkspaceWs(f: TreeNode) {
    if (!dataset) return
    if (!confirm(`确认永久删除 workspace "${f.name}"？\n所有文件、提交历史将被物理删除，无法恢复。`)) return
    try {
      await api.deleteWorkspace(f.id)
      if (curFolder?.id === f.id) { setCurFolder(null); setSelFile(null) }
      const tree = await api.getFolderTree(dataset.id)
      setRootTree(tree)
    } catch (e: any) { setError('Delete workspace failed: ' + e.message) }
  }

  async function openCommitAll() {
    if (!curFolder) return
    try {
      const changes = await api.getWorkspaceChanges(curFolder.id)
      if (changes.length === 0) {
        alert('No uncommitted changes.')
        return
      }
      setCommitAllChanges(changes)
      setCommitAllMsg('')
      setShowCommitAll(true)
    } catch (e: any) { setError('Failed to get changes: ' + e.message) }
  }

  async function confirmCommitAll() {
    if (!curFolder || !dataset || !commitAllMsg.trim()) return
    const files: { file_id: string; file_name: string; operation: string; content: string }[] = []
    for (const ch of commitAllChanges) {
      if (ch.operation === 'delete') {
        files.push({ file_id: ch.file_id, file_name: ch.file_name, operation: 'delete', content: '' })
      } else {
        try {
          const content = await api.getFileContent(ch.file_id)
          files.push({ file_id: ch.file_id, file_name: ch.file_name, operation: ch.operation, content })
        } catch { continue }
      }
    }
    if (files.length === 0) return
    try {
      await api.createCommit(curFolder.id, commitAllMsg, files)
      setShowCommitAll(false)
      setDirty(false)
      setCommits(await api.listFolderCommits(curFolder.id))
    } catch (e: any) { setError('Commit failed: ' + e.message) }
  }

  // ── Knowledge Graph ──
  function openGraphForWorkspace(folderNode: TreeNode) {
    console.log('[Graph] openGraphForWorkspace called, folder:', folderNode.name)
    setCurFolder(folderNode)
    setSelFile(null)
    setShowGraph(true)
    setGraphLoading(true)
    // Start async build
    buildGraphData(folderNode)
  }

  async function buildGraphData(folderNode: TreeNode) {
    console.log('[Graph] buildGraphData started, folder:', folderNode.name)
    try {
      const allFiles: { id: string; name: string }[] = []
      const collect = (node: TreeNode) => {
        if (node.type === 'file' && node.name.endsWith('.md')) {
          allFiles.push({ id: node.id, name: node.name })
        }
        if (node.children) {
          for (const child of node.children) collect(child)
        }
      }
      collect(folderNode)
      console.log('[Graph] found', allFiles.length, 'markdown files:', allFiles.map(f => f.name))

      if (allFiles.length === 0) {
        console.log('[Graph] no markdown files, setting empty graph')
        setGraphNodes([])
        setGraphEdges([])
        setGraphLoading(false)
        return
      }

      const BATCH = 20
      const fileContents: { id: string; name: string; content: string }[] = []
      for (let i = 0; i < allFiles.length; i += BATCH) {
        const batch = allFiles.slice(i, i + BATCH)
        const contents = await Promise.all(
          batch.map(f => api.getFileContent(f.id).then(content => ({ ...f, content }))),
        )
        fileContents.push(...contents)
      }
      console.log('[Graph] fetched', fileContents.length, 'file contents')

      const result = await buildGraph(fileContents)
      console.log('[Graph] buildGraph result:', result.nodes.length, 'nodes,', result.edges.length, 'edges')
      console.log('[Graph] nodes:', result.nodes.map(n => n.id + '(' + n.kind + ')'))
      console.log('[Graph] edges:', result.edges.map(e => e.source + '→' + e.target))
      setGraphNodes(result.nodes)
      setGraphEdges(result.edges)
    } catch (e: any) {
      console.error('[Graph] build error:', e)
      setError('Failed to build graph: ' + e.message)
    } finally {
      setGraphLoading(false)
    }
  }

  function navigateToFile(fileId: string) {
    const find = (node: TreeNode): TreeNode | null => {
      if (node.id === fileId) return node
      if (node.children) {
        for (const child of node.children) {
          const found = find(child)
          if (found) return found
        }
      }
      return null
    }
    const target = rootTree ? find(rootTree) : null
    if (target) selectFile(target)
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

  // ── Tree view expand/collapse ──
  function toggleExpand(folderId: string) {
    setExpandedFolders(prev => {
      const next = new Set(prev)
      if (next.has(folderId)) next.delete(folderId)
      else next.add(folderId)
      return next
    })
  }

  function renderTree(node: TreeNode, depth: number): React.ReactNode {
    if (node.type !== 'folder') return null
    const isExpanded = expandedFolders.has(node.id)
    const isActive = curFolder?.id === node.id
    const children = node.children || []

    return (
      <div key={node.id}>
        <div
          className={`nav-item ${isActive ? 'active' : ''}`}
          style={{ paddingLeft: 8 + depth * 16 }}
        >
          <span className="nav-chevron" onClick={(e) => { e.stopPropagation(); toggleExpand(node.id); }}>
            {isExpanded ? '▾' : '▸'}
          </span>
          <span className="nav-folder-icon" onClick={() => selectFolder(node)}>
            {isExpanded ? '📂' : '📁'}
          </span>
          <span className="nav-label" onClick={() => selectFolder(node)}>{node.name}</span>
          <span className="delete-btn" onClick={(e) => { e.stopPropagation(); deleteWorkspaceWs(node); }}>✕</span>
        </div>
        {isExpanded && children.map(child => {
          if (child.type === 'folder') return renderTree(child, depth + 1)
          if (child.type === 'file' && child.name.endsWith('.md')) {
            const isFileActive = selFile?.id === child.id
            return (
              <div
                key={child.id}
                className={`nav-item sub ${isFileActive ? 'active' : ''}`}
                style={{ paddingLeft: 8 + (depth + 1) * 16 }}
                onClick={() => selectFile(child)}
              >
                <span className="nav-spacer" />
                <span className="nav-file-icon">📄</span>
                <span className="nav-label">{child.name}</span>
                <span className="delete-btn" onClick={(e) => { e.stopPropagation(); deleteItem(child.id, child.name); }}>✕</span>
              </div>
            )
          }
          return null
        })}
      </div>
    )
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-hdr">
          <span className="logo">llmwiki</span>
          <span className="badge">EDIT</span>
          <div style={{ flex: 1 }} />
          <button className="btn-icon-sm" onClick={toggleTheme} title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`} style={{ fontSize: 16 }}>
            {theme === 'dark' ? '☀️' : '🌙'}
          </button>
        </div>

        <div className="section-hdr" style={{padding:'6px 20px'}}>
          <span className="workspace-title" style={{padding:0,textTransform:'uppercase',letterSpacing:'1px'}}>Workspace</span>
          <div style={{display:'flex',gap:2}}>
            <button className="btn-icon-sm" onClick={openCommitAll} title="Commit All Changes">✔</button>
            <button className="btn-icon-sm" onClick={ensureLevel1Folder} title="New Workspace">+</button>
          </div>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '4px 0' }}>
          {folders.map(f => renderTree(f, 0))}
        </div>

        {curFolder && (
          <div style={{ display: 'flex', gap: 2, padding: '4px 12px' }}>
            <button className="btn-icon-sm" onClick={() => setShowNewFile(true)} title="New File" style={{ fontSize: 12 }}>+ Page</button>
            <button className="btn-icon-sm" onClick={() => setShowNewFolder(true)} title="New Folder" style={{ fontSize: 12 }}>📁</button>
            <button className="btn-icon-sm" onClick={() => { if (curFolder) openGraphForWorkspace(curFolder); }} title="Knowledge Graph" style={{ fontSize: 12 }}>🕸</button>
            <button className="btn-icon-sm" onClick={loadCommits} title="History" style={{ fontSize: 12 }}>🕐</button>
          </div>
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

        {curFolder && !selFile && !showGraph && (
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

        {showGraph && curFolder && (
          <div style={{ display: 'flex', flexDirection: 'column', flex: 1, overflow: 'hidden' }}>
            <div className="editor-toolbar">
              <div className="editor-toolbar-left">
                <span className="editor-breadcrumb">Graph: <strong>{curFolder.name}</strong></span>
              </div>
              <div className="toolbar-actions">
                <button className="btn" onClick={() => setShowGraph(false)} style={{ padding: '4px 12px', fontSize: 12 }}>
                  ✕ Close
                </button>
              </div>
            </div>
            <div style={{ flex: 1, minHeight: 0, position: 'relative' }}>
              {graphLoading ? (
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--nim-text-faint)', gap: 8 }}>
                  <span style={{ fontSize: 18 }}>🕸</span> Building graph...
                </div>
              ) : (
                <GraphView nodes={graphNodes} edges={graphEdges} onNavigate={navigateToFile} />
              )}
            </div>
          </div>
        )}

        {selFile && !showGraph && (
          <div className="editor-view">
            <div className="editor-toolbar">
              <div className="editor-toolbar-left">
                <span className="editor-breadcrumb">{curFolder?.name} / <strong>{selFile.name}</strong></span>
              </div>
              <div className="toolbar-actions">
                {dirty && <button className="btn-primary" onClick={() => setShowCommitDlg(true)}>Commit Changes</button>}
              </div>
            </div>
            <MarkdownEditor
              key={selFile.id}
              content={content}
              onChange={text => { setContent(text); setDirty(text !== origContent) }}
              fileName={selFile?.name}
            />
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

      {/* Commit All dialog */}
      {showCommitAll && (
        <div className="overlay" onClick={() => setShowCommitAll(false)}>
          <div className="dialog dialog-commit" onClick={e => e.stopPropagation()}>
            <h3>✔ Commit All Changes</h3>
            <div className="commit-summary">
              {commitAllChanges.map(ch => (
                <div key={ch.file_id} className="commit-file-row" style={{marginBottom:4}}>
                  <span>📄 {ch.file_name}</span>
                  <span className={`badge ${ch.operation === 'delete' ? 'badge-modify' : ''}`}>{ch.operation}</span>
                </div>
              ))}
            </div>
            <input className="input" value={commitAllMsg} onChange={e => setCommitAllMsg(e.target.value)}
              placeholder="Describe your changes..." autoFocus
              onKeyDown={e => e.key === 'Enter' && confirmCommitAll()} />
            <div className="dlg-actions">
              <button className="btn" onClick={() => setShowCommitAll(false)}>Cancel</button>
              <button className="btn-primary" onClick={confirmCommitAll} disabled={!commitAllMsg.trim()}>Commit</button>
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

function fmtTime(ts?: number): string {
  return ts ? new Date(ts).toLocaleString('zh-CN') : ''
}

function formatSize(bytes?: number): string {
  if (!bytes) return ''
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}
