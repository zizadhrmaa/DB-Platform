export interface APIErrorDetail {
  code: string;
  message: string;
  fields?: Record<string, string>;
}

export interface APIErrorResponse {
  error: APIErrorDetail;
}

export async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers || {});
  headers.set('Accept', 'application/json');

  const res = await fetch(path, {
    ...options,
    headers,
    credentials: 'include',
  });

  if (!res.ok) {
    let errDetail: APIErrorDetail = {
      code: 'http_error',
      message: `Permintaan gagal dengan status ${res.status}`,
    };
    try {
      const data: APIErrorResponse = await res.json();
      if (data && data.error) {
        errDetail = data.error;
      }
    } catch {
      // bukan JSON error
    }
    const err = new Error(errDetail.message) as Error & { code: string; status: number; fields?: Record<string, string> };
    err.code = errDetail.code;
    err.status = res.status;
    err.fields = errDetail.fields;
    throw err;
  }

  return res.json() as Promise<T>;
}

export interface DatabaseSummary {
  id: number;
  name: string;
  db_type: string;
  level: string;
}

export interface TableItem {
  schema: string;
  name: string;
  kind: string;
}

export interface SchemaGroup {
  schema: string;
  tables: TableItem[];
}

export interface DatabaseShowResponse {
  db: {
    id: number;
    name: string;
    level: string;
  };
  schemas: SchemaGroup[];
}

export interface ColumnDetail {
  name: string;
  type: string;
  nullable: boolean;
  default: string;
}

export interface ConstraintDetail {
  name: string;
  type: string;
  definition: string;
  references: string;
}

export interface IndexDetail {
  name: string;
  unique: boolean;
  primary: boolean;
  definition: string;
}

export interface TableDetailResponse {
  db: {
    id: number;
    name: string;
    level: string;
  };
  table: {
    schema: string;
    name: string;
    kind: string;
    columns: ColumnDetail[];
    constraints: ConstraintDetail[];
    indexes: IndexDetail[];
  };
}

export interface DataCell {
  text: string;
  null: boolean;
  truncated: boolean;
}

export interface TableDataResponse {
  db: {
    id: number;
    name: string;
    level: string;
  };
  page: {
    schema: string;
    name: string;
    kind: string;
    columns: string[];
    rows: DataCell[][];
    page: number;
    size: number;
    total: number;
    total_estimate: boolean;
    has_prev: boolean;
    has_next: boolean;
    page_count: number;
    ordered_by: string;
  };
}
