import { getCsrfToken, notifyUnauthorized } from './session';

export interface APIErrorDetail {
  code: string;
  message: string;
  fields?: Record<string, string>;
}

export interface APIErrorResponse {
  error: APIErrorDetail;
}

export class ApiError extends Error {
  code: string;
  status: number;
  fields?: Record<string, string>;

  constructor(message: string, code: string, status: number, fields?: Record<string, string>) {
    super(message);
    this.code = code;
    this.status = status;
    this.fields = fields;
  }
}

type ApiOptions = RequestInit & { json?: unknown };

export async function apiFetch<T>(path: string, options: ApiOptions = {}): Promise<T> {
  const { json, ...init } = options;
  const method = (init.method || 'GET').toUpperCase();
  const headers = new Headers(init.headers || {});
  headers.set('Accept', 'application/json');
  if (json !== undefined) {
    headers.set('Content-Type', 'application/json');
    init.body = JSON.stringify(json);
  }
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    const token = getCsrfToken();
    if (token) headers.set('X-CSRF-Token', token);
  }

  const res = await fetch(path, { ...init, method, headers, credentials: 'same-origin' });

  if (res.status === 401) notifyUnauthorized();

  if (!res.ok) {
    let detail: APIErrorDetail = {
      code: 'http_error',
      message: `Permintaan gagal dengan status ${res.status}`,
    };
    try {
      const data: APIErrorResponse = await res.json();
      if (data && data.error) detail = data.error;
    } catch {
      detail = { ...detail };
    }
    throw new ApiError(detail.message, detail.code, res.status, detail.fields);
  }

  if (res.status === 204) return undefined as T;
  const type = res.headers.get('Content-Type') || '';
  if (!type.includes('application/json')) {
    throw new ApiError('Respons server tidak valid.', 'invalid_response', res.status);
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
