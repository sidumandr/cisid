// The shapes the JSON API returns; see docs/api.md.

export type JobStatus = "pending" | "running" | "success" | "failed" | "canceled";
export type StepState = "pending" | "running" | "success" | "failed" | "canceled" | "skipped";

export interface StepResult {
  state: StepState;
  exit_code?: number;
  started_at?: string;
  finished_at?: string;
}

export interface Job {
  id: number;
  repo: string;
  clone_url: string;
  branch: string;
  commit_sha: string;
  status: JobStatus;
  created_at: string;
  event: "push" | "pull_request" | "manual" | "schedule";
  pull_request?: number;
  target_branch?: string;
  fork?: boolean;
  agent?: string;
  started_at?: string;
  finished_at?: string;
  steps?: string[];
  matrix?: Record<string, string>;
  parent_id?: number;
  exit_code?: number;
  failed_step?: number;
  error?: string;
  logs_expired_at?: string;
  current_step?: number;
  step_results?: StepResult[];
  labels?: string[];
  needs_approval?: boolean;
  approved_by?: string;
}

export interface JobLog {
  job_id: number;
  step: number;
  stream: "stdout" | "stderr";
  seq: number;
  data: string;
}

export interface Artifact {
  job_id: number;
  name: string;
  size: number;
  sha256: string;
  created_at: string;
}

export interface Schedule {
  repo: string;
  name: string;
  cron: string;
  branch?: string;
  next_run_at: string;
  last_run_at?: string;
  last_job_id?: number;
}

// RepositoryInfo is a repository connected to cisid.
export interface RepositoryInfo {
  repo: string;
  clone_url: string;
  default_branch: string;
  added_by?: string;
  added_at: string;
  polled: boolean;
}

// Connected is the answer to connecting a repository.
export interface Connected extends RepositoryInfo {
  has_pipeline: boolean;
}

// Reachable lists the repositories the server's forge tokens reach.
export interface Reachable {
  repositories: { repo: string; forge: string; private: boolean }[];
  unlisted: string[];
}

export interface Watch {
  repo: string;
  branch: string;
  last_sha?: string;
  last_poll_at?: string;
  last_job_id?: number;
  last_error?: string;
  created_at: string;
}

export interface SecretInfo {
  name: string;
  updated_at: string;
  branches?: string[];
}

export interface Notification {
  repo: string;
  name: string;
  kind: "slack" | "webhook" | "email";
  target: string;
  on: string[];
  branches?: string[];
  pull_requests: boolean;
  last_sent_at?: string;
  last_error?: string;
  updated_at: string;
}

export type Role = "viewer" | "maintainer" | "admin";

export interface User {
  username: string;
  role: Role;
  disabled?: boolean;
  has_password: boolean;
  identities?: Record<string, string>;
  created_at: string;
}

export interface APIToken {
  id: number;
  name: string;
  created_at: string;
  last_used_at?: string;
}

// Me is what GET /api/me says about the caller and the server.
export interface Me {
  user?: User;
  role?: Role;
  via?: "session" | "token" | "read cookie";
  public_read: boolean;
  accounts: boolean;
  providers?: string[];
  signup?: "first" | "open";
  provider_signup?: boolean;
}
