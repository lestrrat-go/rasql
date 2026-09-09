CREATE TABLE members (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL);
CREATE TABLE projects (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, assignee_id INTEGER, title VARCHAR(128) NOT NULL, is_open BOOLEAN NOT NULL DEFAULT TRUE, due_on DATE, created_at TIMESTAMP NOT NULL DEFAULT '2024-01-01T00:00:00Z');
CREATE TABLE task_labels (task_id INTEGER NOT NULL, label VARCHAR(32) NOT NULL, PRIMARY KEY (task_id, label));
CREATE INDEX tasks_project_open_id ON tasks(project_id, is_open, id);
CREATE INDEX tasks_assignee ON tasks(assignee_id, id);
