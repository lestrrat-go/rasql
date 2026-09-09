-- The shape settled in chapter 02, exactly as it was run against the
-- working database. Re-run this file to rebuild that database from empty.

CREATE TABLE members (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL
);

CREATE TABLE projects (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL
);

CREATE TABLE tasks (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  assignee_id bigint NOT NULL REFERENCES members (id) ON DELETE NO ACTION,
  title text NOT NULL,
  is_open boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tasks_open_by_project ON tasks (project_id, id) WHERE is_open;
