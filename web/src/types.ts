// === Types matching backend entity models ===

export interface FileCommit {
  id: string
  folder_id: string
  parent_id?: string
  message: string
  author_id: string
  file_count: number
  tree_state?: string
  create_time?: number
  create_date?: string
}

export interface FileCommitItem {
  id: string
  commit_id: string
  file_id: string
  operation: 'add' | 'modify' | 'delete' | 'rename'
  old_hash?: string
  new_hash?: string
  old_location?: string
  new_location?: string
  old_name?: string
  new_name?: string
}

export interface TreeNode {
  id: string
  name: string
  type: 'folder' | 'file'
  children?: TreeNode[]
  content_hash?: string
  location?: string
  size?: number
}

export interface Dataset {
  id: string
  tenant_id: string
  name: string
  embd_id: string
  created_by: string
  doc_num: number
  permission: string
  status?: string
  create_time?: number
}

export interface DocsFile {
  id: string
  parent_id: string
  tenant_id: string
  created_by: string
  name: string
  location?: string
  size: number
  type: string
  source_type: string
}

export interface Document {
  id: string
  kb_id: string
  name?: string
  type: string
  suffix: string
  created_by: string
  content_hash?: string
  size: number
}
