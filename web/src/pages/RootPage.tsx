import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import type { TreeNode, Dataset } from '../types'
import * as api from '../api'

export default function RootPage() {
  const navigate = useNavigate()
  const [rootTree, setRootTree] = useState<TreeNode | null>(null)
  const [dataset, setDataset] = useState<Dataset | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => { load() }, [])

  async function load() {
    try {
      let ds = await api.listDatasets()
      if (!ds || ds.length === 0) ds = [await api.createDataset('llmwiki')]
      setDataset(ds[0])
      const tree = await api.getFolderTree(ds[0].id)
      setRootTree(tree)
    } catch {}
    setLoading(false)
  }

  async function createFirst() {
    if (!dataset || !rootTree) return
    await api.createFolder(rootTree.id, 'My Workspace')
    const tree = await api.getFolderTree(dataset.id)
    setRootTree(tree)
  }

  function goWorkspace(folder: TreeNode) {
    navigate(`/${encodeURIComponent(folder.name)}`)
  }

  const folders = rootTree?.children?.filter(c => c.type === 'folder') || []

  if (loading) return (
    <div className="app">
      <main className="main">
        <div className="welcome"><h1>llmwiki</h1><p>Loading...</p></div>
      </main>
    </div>
  )

  return (
    <div className="app">
      <main className="main">
        <div className="welcome">
          <h1>📝 llmwiki</h1>
          <p style={{marginBottom:24}}>A version-controlled wiki powered by Markdown</p>

          {folders.length === 0 ? (
            <>
              <p className="hint">No workspaces yet</p>
              <button className="btn-primary" style={{marginTop:12}} onClick={createFirst}>
                + Create Your First Workspace
              </button>
              {rootTree && (
                <button className="btn-link" style={{marginTop:8}} onClick={goWorkspace.bind(null, rootTree)}>
                  (or view root folder)
                </button>
              )}
            </>
          ) : (
            <>
              <p className="hint" style={{marginBottom:16}}>Select a workspace:</p>
              <div style={{display:'flex',flexDirection:'column',gap:8,minWidth:280}}>
                {folders.map(f => (
                  <div key={f.id} className="file-card" style={{cursor:'pointer',padding:'16px 20px',flexDirection:'row'}}
                    onClick={() => goWorkspace(f)}>
                    <span style={{fontSize:20}}>📁</span>
                    <span style={{fontSize:15,fontWeight:500,marginLeft:8}}>{f.name}</span>
                  </div>
                ))}
              </div>
              <button className="btn-link" style={{marginTop:16}} onClick={createFirst}>
                + New Workspace
              </button>
            </>
          )}
        </div>
      </main>
    </div>
  )
}
