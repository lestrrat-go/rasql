CREATE INDEX "tasks_open_by_project" ON "tasks" ("project_id", "id") WHERE is_open;
