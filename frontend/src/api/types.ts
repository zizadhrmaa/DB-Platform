export interface Me {
  id: number;
  username: string;
  is_platform_admin: boolean;
  csrf_token: string;
}

export interface GroupRef {
  id: number;
  name: string;
}

export interface AdminUser {
  id: number;
  username: string;
  is_platform_admin: boolean;
  is_active: boolean;
  created_at: string;
  group_count: number;
  groups: GroupRef[];
}

export interface AdminGroup {
  id: number;
  name: string;
  description: string;
  member_count: number;
  access_count: number;
  leader_user_id: number | null;
}

export interface Member {
  user_id: number;
  username: string;
  is_active: boolean;
}

export interface AccessEntry {
  database_id: number;
  database_name: string;
  database_active: boolean;
  level: 'view' | 'edit';
}

export interface DbOption {
  id: number;
  name: string;
  is_active: boolean;
}

export interface GroupDetail extends AdminGroup {
  members: Member[];
  access: AccessEntry[];
  candidates: Member[];
  databases: DbOption[];
}

export interface Connection {
  id: number;
  name: string;
  db_type: string;
  host: string;
  port: number;
  database: string;
  username: string;
  sslmode: string;
  is_active: boolean;
}

export type ChangeStatus = 'draft' | 'pending_review' | 'approved' | 'queued' | 'rejected' | 'cancelled';

export interface ChangeRequest {
  id: number;
  database_id: number;
  migration_id: number;
  version: number;
  title: string;
  revision: number;
  stale: boolean;
  kind: 'up' | 'rollback' | 'forward';
  target_execution_id: number | null;
  status: ChangeStatus;
  requested_by: string;
  reject_reason: string;
  job_id: number | null;
  job_status: string;
  created_at: string;
  submitted_at: string | null;
  updated_at: string;
}

export interface Approval {
  group_id: number;
  group_name: string;
  leader_id: number | null;
  leader: string;
  status: 'pending' | 'approved' | 'rejected';
  decided_by: string;
  decided_at: string | null;
}

export interface ChangeDetail extends ChangeRequest {
  sql: string;
  target_sql: string;
  approvals: Approval[];
}