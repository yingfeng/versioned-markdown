/**
 * wiki-graph.ts — build entity-level knowledge graph from wiki files.
 *
 * Nodes = entities ([[wikilinks]] that don't correspond to .md files)
 * Edges = co-occurrence (two entities mentioned in the same page)
 * Page nodes are NOT included — only entity-level relationships.
 */

export interface GraphNode {
  id: string
  label: string
  kind: 'entity'
  linkCount: number    // number of pages mentioning this entity
  sourcePages?: { fileId: string; label: string }[]  // pages that reference this entity
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
  const pageEntityIds: string[] = []  // file names as IDs for lookup
  const pageMap = new Map<string, string>() // normalized filename → original name

  for (const f of files) {
    if (!f.name.toLowerCase().endsWith('.md')) continue
    const id = normalize(f.name.replace(/\.md$/i, ''))
    pageEntityIds.push(id)
    pageMap.set(id, f.name)
  }

  // Step 1: For each page, collect all [[wikilinks]] that DON'T resolve to a page
  const pageEntities = new Map<string, Set<string>>() // normalized page id → set of entity normalized ids
  const pageInfo = new Map<string, { fileId: string; label: string }>() // normalized page id → file info

  for (const f of files) {
    if (!f.name.toLowerCase().endsWith('.md')) continue
    const pageId = normalize(f.name.replace(/\.md$/i, ''))
    const headingMatch = f.content.match(/^#\s+(.+)$/m)
    const pageLabel = headingMatch ? headingMatch[1].trim() : f.name.replace(/\.md$/i, '').replace(/-/g, ' ')
    pageInfo.set(pageId, { fileId: f.id, label: pageLabel })

    const wikilinks = extractWikilinks(f.content)
    const entities = new Set<string>()

    for (const raw of wikilinks) {
      const norm = normalize(raw)
      const isPage = pageEntityIds.some(pid => pid === norm || normalize(pid) === norm)
      if (!isPage) {
        entities.add(norm)
      }
    }

    if (entities.size > 0) {
      pageEntities.set(pageId, entities)
    }
  }

  // Step 2: Build entity metadata (original label)
  const entityLabels = new Map<string, string>()
  for (const [, entities] of pageEntities) {
    for (const norm of entities) {
      if (!entityLabels.has(norm)) {
        // Try to recover original label from any file content
        entityLabels.set(norm, norm.replace(/-/g, ' '))
      }
    }
  }

  // Try to find original labels in source texts (heuristic)
  for (const f of files) {
    if (!f.name.toLowerCase().endsWith('.md')) continue
    const wikilinks = extractWikilinks(f.content)
    for (const raw of wikilinks) {
      const norm = normalize(raw)
      if (entityLabels.has(norm) && raw !== entityLabels.get(norm)) {
        // Use the version that appears in the text (might be more human-readable)
        if (raw.length > entityLabels.get(norm)!.length) {
          entityLabels.set(norm, raw)
        }
      }
    }
  }

  // Step 3: Co-occurrence counting
  // For each page, pair up all entities → increment co-occurrence count
  const coocCount = new Map<string, number>() // "entityA:::entityB" → count
  const pageCount = new Map<string, number>() // entity → how many pages mention it

  for (const [, entities] of pageEntities) {
    const list = Array.from(entities)
    // Increment page count for each entity
    for (const e of list) {
      pageCount.set(e, (pageCount.get(e) ?? 0) + 1)
    }
    // Pair all entities for co-occurrence
    for (let i = 0; i < list.length; i++) {
      for (let j = i + 1; j < list.length; j++) {
        const key = `${list[i]}:::${list[j]}`
        coocCount.set(key, (coocCount.get(key) ?? 0) + 1)
      }
    }
  }

  // Step 4: Build nodes with source page info
  const entityRefs = new Map<string, { fileId: string; label: string }[]>()
  for (const [pageId, entities] of pageEntities) {
    const info = pageInfo.get(pageId)
    if (!info) continue
    for (const norm of entities) {
      const refs = entityRefs.get(norm) ?? []
      if (!refs.some(r => r.fileId === info.fileId)) {
        refs.push(info)
      }
      entityRefs.set(norm, refs)
    }
  }

  const nodes: GraphNode[] = []
  for (const [norm, label] of entityLabels) {
    const count = pageCount.get(norm) ?? 0
    if (count > 0) {
      nodes.push({
        id: norm,
        label,
        kind: 'entity',
        linkCount: count,
        sourcePages: entityRefs.get(norm),
      })
    }
  }

  // Step 5: Build edges (co-occurrence edges with weight)
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
