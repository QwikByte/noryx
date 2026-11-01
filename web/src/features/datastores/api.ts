import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query"
import { type Operation, operate } from "@/features/operations/api"
import { api, send } from "@/lib/api"

export type Engine = "mariadb" | "postgres"

export type DatastoreState = "stopped" | "starting" | "running" | "unhealthy" | "unknown"

/** A database of a datastore, with a user of the same name, whose password only those who manage datastores see. */
export interface Database {
  name: string
  createdAt: string
}

/** A MariaDB or PostgreSQL server of a network on a node, as its agent reports it. */
export interface Datastore {
  id: string
  networkId: string
  nodeId: string
  nodeName: string
  name: string
  engine: Engine
  version: string
  memoryMb: number
  cpuMillis: number
  storage: string
  /** Published in the private network of the nodes for the servers of other nodes; 0 until one needs it. */
  port: number
  createdAt: string
  databases: Database[]
  state: DatastoreState
  size: number
  /** The version whose data an upgrade left, until it is removed. */
  previous?: string
  /** The major versions of its engine that the agent runs, oldest first. */
  versions: string[]
  /** Databases that the datastore lacks, e.g. after its data was replaced. */
  missing: string[]
  problem?: string
  /** Where the servers of the network reach it. */
  endpoints: Endpoint[]
  /** The most connections at once; 0 for the default of its engine. */
  maxConnections: number
  /** MB of InnoDB's buffer pool or PostgreSQL's shared buffers; 0 for the default, which follows the memory. */
  bufferMb: number
}

/** The bounds and defaults of the parameters of an engine with memoryMb, as the master checks them. */
export function parameterBounds(engine: Engine, memoryMb: number) {
  const postgres = engine === "postgres"
  return {
    connections: { min: 10, max: Math.min(Math.floor(postgres ? memoryMb / 2 : memoryMb), 1000), default: postgres ? 100 : 151 },
    bufferMb: { min: 32, max: memoryMb - Math.floor(memoryMb / 4), default: Math.max(Math.floor(memoryMb / (postgres ? 4 : 2)), 128) },
  }
}

/** Where servers reach a datastore: those on its node by the name of its container, those of other nodes over the private network of the nodes. */
interface Endpoint {
  host: string
  port: number
  remote: boolean
}

/** A table of a database; PostgreSQL's have a schema. */
export interface Table {
  schema?: string
  name: string
  /** Estimated; -1 if unknown. */
  rows: number
  size: number
}

/** Rows of a table as text, with long values cut short. */
export interface Page {
  columns: { name: string; type: string; primaryKey?: boolean }[]
  rows: { text: string; null?: boolean; truncated?: boolean }[][]
  more: boolean
}

/** The rows a page of a table has. */
export const pageRows = 50

export interface Dump {
  id: string
  label: string
  createdAt: string
  size: number
  location: string
  databases: string[]
  jobId?: string
}

export interface DatastoreInput {
  name: string
  nodeId: string
  engine: Engine
  version: string
  memoryMb: number
  cpuMillis: number
  storage: string
}

export interface DatastoreChange {
  memoryMb?: number
  cpuMillis?: number
  /** 0 for the defaults. */
  maxConnections?: number
  bufferMb?: number
  version?: string
  updateImage?: boolean
  removePrevious?: boolean
}

export const datastoresQuery = queryOptions({
  queryKey: ["datastores"],
  queryFn: () => api<Datastore[]>("/datastores"),
})

export const networkDatastoresQuery = (networkId: string) =>
  queryOptions({
    queryKey: ["datastores", "network", networkId],
    queryFn: () => api<Datastore[]>(`/networks/${networkId}/datastores`),
    refetchInterval: 10_000,
  })

const databasePath = (id: string, database: string) => `/datastores/${id}/databases/${database}`

/** The password of a database's user, which is fetched each time it is shown and never kept. */
export const passwordQuery = (id: string, database: string) =>
  queryOptions({
    queryKey: ["datastores", id, "databases", database, "password"],
    queryFn: () => api<{ password: string }>(`${databasePath(id, database)}/password`),
    gcTime: 0,
    staleTime: 0,
  })

export const tablesQuery = (id: string, database: string) =>
  queryOptions({
    queryKey: ["datastores", id, "databases", database, "tables"],
    queryFn: () => api<Table[]>(`${databasePath(id, database)}/tables`),
  })

/** Rows whose value of a column, as text like a page shows it, equals a value or contains it, ignoring case. */
export interface TableFilter {
  column: string
  value: string
  contains: boolean
}

/** Which rows of a table a page shows: from offset on, sorted by a column and then by the primary key, and those that a filter matches. */
export interface TableView {
  offset: number
  sort?: string
  descending?: boolean
  filter?: TableFilter
}

export const pageQuery = (id: string, database: string, table: Table, view: TableView) =>
  queryOptions({
    queryKey: ["datastores", id, "databases", database, "tables", table.schema, table.name, view],
    queryFn: () => {
      const query = new URLSearchParams({ schema: table.schema ?? "", offset: String(view.offset) })
      if (view.sort) query.set("sort", view.sort)
      if (view.sort && view.descending) query.set("descending", "true")
      if (view.filter) {
        query.set("filter", view.filter.column)
        query.set("value", view.filter.value)
        if (view.filter.contains) query.set("contains", "true")
      }
      return api<Page>(`${databasePath(id, database)}/tables/${encodeURIComponent(table.name)}?${query}`)
    },
    placeholderData: (previous) => previous,
  })

export const dumpsQuery = (id: string) =>
  queryOptions({
    queryKey: ["datastores", id, "backups"],
    queryFn: () => api<Dump[]>(`/datastores/${id}/backups`),
  })

export const downloadUrl = (id: string, dump: string) => `/api/datastores/${id}/backups/${dump}/download`

/** The most bytes of an uploaded dump, as the master allows. */
export const maxUpload = 16 * 1024 ** 3

/**
 * Uploads a dump made elsewhere with progress reports, which fetch can't make: the SQL of database, or a ZIP archive
 * with a <database>.sql for each database without.
 */
function uploadDump(
  id: string,
  file: File,
  { database, label, ...progress }: { database?: string; label: string; onProgress?: (fraction: number) => void; signal?: AbortSignal },
): Promise<Dump> {
  return send<Dump>("POST", `/datastores/${id}/backups/upload?${new URLSearchParams({ label, ...(database && { database }) })}`, file, progress)
}

type Started = { onStart?: (op: Operation) => void }

export function useCreateDatastore(networkId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ onStart, ...input }: DatastoreInput & Started) => operate<Datastore>(`/networks/${networkId}/datastores`, { body: input }, onStart),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["datastores"] }),
  })
}

/** Changes a datastore, its databases and its dumps. */
export function useDatastore(id: string) {
  const queryClient = useQueryClient()
  const onSettled = () => queryClient.invalidateQueries({ queryKey: ["datastores"] })
  const base = `/datastores/${id}`
  return {
    update: useMutation({
      mutationFn: ({ onStart, ...change }: DatastoreChange & Started) => operate<Datastore>(base, { method: "PATCH", body: change }, onStart),
      onSettled,
    }),
    power: useMutation({ mutationFn: (action: "start" | "stop") => api(`${base}/${action}`, { method: "POST" }), onSettled }),
    remove: useMutation({ mutationFn: () => api(base, { method: "DELETE" }), onSettled }),
    addDatabase: useMutation({ mutationFn: (name: string) => api<Database>(`${base}/databases`, { body: { name } }), onSettled }),
    dropDatabase: useMutation({ mutationFn: (name: string) => api(`${base}/databases/${name}`, { method: "DELETE" }), onSettled }),
    rotate: useMutation({ mutationFn: (name: string) => api(`${base}/databases/${name}/rotate`, { method: "POST" }), onSettled }),
    dump: useMutation({
      mutationFn: ({ onStart, ...input }: { label: string; databases: string[] } & Started) =>
        operate<Dump>(`${base}/backups`, { body: input }, onStart),
      onSettled,
    }),
    restore: useMutation({
      mutationFn: ({ dump, databases, onStart }: { dump: string; databases: string[] } & Started) =>
        operate(`${base}/backups/${dump}/restore`, { body: { databases } }, onStart),
      onSettled: () => {
        void onSettled()
        void queryClient.invalidateQueries({ queryKey: ["nodes"] })
      },
    }),
    removeDump: useMutation({ mutationFn: (dump: string) => api(`${base}/backups/${dump}`, { method: "DELETE" }), onSettled }),
    upload: useMutation({ mutationFn: ({ file, ...options }: { file: File } & Parameters<typeof uploadDump>[2]) => uploadDump(id, file, options), onSettled }),
  }
}
