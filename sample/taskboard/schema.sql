PRAGMA foreign_keys = ON;

CREATE TABLE members (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE
);

CREATE TABLE projects (
    id INTEGER PRIMARY KEY,
    owner_id INTEGER NOT NULL REFERENCES members(id),
    name TEXT NOT NULL,
    archived BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE tasks (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    assignee_id INTEGER NOT NULL REFERENCES members(id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('todo', 'in_progress', 'done')),
    priority INTEGER NOT NULL
);

CREATE INDEX tasks_open_by_project ON tasks(project_id, status, priority);
