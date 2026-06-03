/**
 * wiki-graph.ts — build entity + page knowledge graph from wiki files.
 *
 * Two kinds of nodes:
 *   - entity: [[wikilinks]] that don't correspond to any .md file
 *   - page:   [[wikilinks]] that DO correspond to an existing .md file
 *
 * Edges = co-occurrence (two nodes mentioned in the same page).
 */

export interface GraphNode {
  id: string
  label: string
  kind: 'entity' | 'page'
  linkCount: number    // number of pages mentioning this entity
  sourcePages?: { fileId: string; label: string }[]  // pages that reference this node
}

export interface GraphEdge {
  source: string
  target: string
  weight: number       // co-occurrence count
  label: string        // "co-occurs in X pages"
}

const WIKILINK_REGEX = /\[\[([^\]|]+?)(?:\|[^\]]+?)?\]\]/g

function extractWikilinks(content: string): string[] {
  const links: string[] = []
  const re = new RegExp(WIKILINK_REGEX.source, 'g')
  let m: RegExpExecArray | null
  while ((m = re.exec(content)) !== null) {
    links.push(m[1].trim())
  }
  return links
}

function normalize(raw: string): string {
  return raw.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9\u4e00-\u9fff-]/g, '')
}

export async function buildGraph(
  files: { id: string; name: string; content: string }[],
): Promise<{ nodes: GraphNode[]; edges: GraphEdge[] }> {
  // Build page index: normalized name → original name
  const pageIds = new Set<string>()
  const pageMap = new Map<string, { name: string; base: string }>() // norm → {name, base}

  for (const f of files) {
    if (!f.name.toLowerCase().endsWith('.md')) continue
    const base = f.name.replace(/\.md$/i, '')
    const id = normalize(base)
    pageIds.add(id)
    if (!pageMap.has(id)) {
      pageMap.set(id, { name: f.name, base })
    }
  }

  // Step 1: For each page, collect ALL [[wikilinks]]
  // Track both entity links (no matching page) and page links (matching page)
  const pageLinks = new Map<string, Set<string>>() // normalized page id → set of linked normalized ids
  const pageInfo = new Map<string, { fileId: string; label: string }>() // normalized page id → file info

  for (const f of files) {
    if (!f.name.toLowerCase().endsWith('.md')) continue
    const base = f.name.replace(/\.md$/i, '')
    const pageId = normalize(base)
    const headingMatch = f.content.match(/^#\s+(.+)$/m)
    const pageLabel = headingMatch ? headingMatch[1].trim() : base.replace(/-/g, ' ')
    pageInfo.set(pageId, { fileId: f.id, label: pageLabel })

    const wikilinks = extractWikilinks(f.content)
    const linked = new Set<string>()

    for (const raw of wikilinks) {
      const norm = normalize(raw)
      linked.add(norm) // Collect ALL links (both page and entity)
    }

    if (linked.size > 0) {
      pageLinks.set(pageId, linked)
    } else {
      console.log('[Graph] NO links found in:', pageId, 'content length:', f.content.length, 'first 100:', f.content.substring(0,100))
    }
  }

  console.log('[Graph] pageLinks entries:', Array.from(pageLinks.entries()).map(([k,v]) => k + '→[' + Array.from(v).join(',') + ']'))

  // Step 2: Classify each link as page or entity
  // A link is a "page" if it matches a filename, otherwise it's an "entity"
  const nodeKinds = new Map<string, 'entity' | 'page'>()
  const entityLabels = new Map<string, string>()
  const nodePageRefs = new Map<string, Set<string>>() // norm → set of file IDs
  const pageCount = new Map<string, number>() // node → how many pages mention it

  // First, classify all referenced nodes + collect page labels
  for (const [srcPage, linked] of pageLinks) {
    for (const norm of linked) {
      const isPage = pageIds.has(norm)
      const kind = isPage ? 'page' : 'entity'
      nodeKinds.set(norm, kind)

      // Label: use wikilink name (e.g. "machine-learning") for both pages and entities
      if (!entityLabels.has(norm)) {
        entityLabels.set(norm, norm.replace(/-/g, ' '))
      }

      // Track which pages reference this node
      const srcInfo = pageInfo.get(srcPage)
      if (srcInfo) {
        if (!nodePageRefs.has(norm)) nodePageRefs.set(norm, new Set())
        nodePageRefs.get(norm)!.add(srcInfo.fileId)
      }
    }
  }

  // Add all pages as nodes — even if they're never referenced by anyone
  for (const [pageId, info] of pageInfo) {
    if (!nodeKinds.has(pageId)) {
      nodeKinds.set(pageId, 'page')
      if (!entityLabels.has(pageId)) {
        entityLabels.set(pageId, pageId.replace(/-/g, ' '))
      }
    }
    // Ensure pageCount >= 1 for every page (it exists)
    if (!pageCount.has(pageId)) {
      pageCount.set(pageId, 1)
    }
  }

  // Recover original labels from source text for entities
  for (const f of files) {
    if (!f.name.toLowerCase().endsWith('.md')) continue
    const wikilinks = extractWikilinks(f.content)
    for (const raw of wikilinks) {
      const norm = normalize(raw)
      if (nodeKinds.get(norm) === 'entity' && entityLabels.has(norm) && raw !== entityLabels.get(norm)) {
        if (raw.length > entityLabels.get(norm)!.length) {
          entityLabels.set(norm, raw)
        }
      }
    }
  }

  // Step 3: Build co-occurrence data
  const coocCount = new Map<string, number>()

  for (const [, linked] of pageLinks) {
    const list = Array.from(linked)

    // Increment page count for each node
    for (const n of list) {
      pageCount.set(n, (pageCount.get(n) ?? 0) + 1)
    }

    // Pair all nodes for co-occurrence (both page and entity)
    for (let i = 0; i < list.length; i++) {
      for (let j = i + 1; j < list.length; j++) {
        const key = `${list[i]}:::${list[j]}`
        coocCount.set(key, (coocCount.get(key) ?? 0) + 1)
      }
    }
  }

  // Step 4: Build nodes
  const nodes: GraphNode[] = []
  for (const [norm, label] of entityLabels) {
    const count = pageCount.get(norm) ?? 0
    if (count > 0) {
      const refs = nodePageRefs.get(norm)
      const sourcePages = refs
        ? Array.from(refs).map(fid => ({ fileId: fid, label: pageInfo.get(findPageKey(pageInfo, fid))?.label ?? norm }))
        : undefined
      nodes.push({
        id: norm,
        label,
        kind: nodeKinds.get(norm) ?? 'entity',
        linkCount: count,
        sourcePages,
      })
    }
  }

  // Step 5: Build edges
  const edges: GraphEdge[] = []
  for (const [key, count] of coocCount) {
    const [source, target] = key.split(':::')
    edges.push({
      source,
      target,
      weight: count,
      label: count > 1 ? `${count} pages` : '1 page',
    })
  }

  return { nodes, edges }
}

function findPageKey(m: Map<string, { fileId: string; label: string }>, fileId: string): string {
  for (const [k, v] of m) {
    if (v.fileId === fileId) return k
  }
  return ''
}
