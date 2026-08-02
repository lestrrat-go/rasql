CREATE TABLE "tasks" (
  "id" INTEGER NOT NULL,
  "project_id" INTEGER NOT NULL,
  "assignee_id" INTEGER NOT NULL,
  "title" TEXT NOT NULL,
  "status" TEXT NOT NULL,
  "priority" INTEGER NOT NULL,
  PRIMARY KEY ("id"),
  CHECK (status IN ('todo', 'in_progress', 'done')),
  FOREIGN KEY ("project_id") REFERENCES "projects" ("id"),
  FOREIGN KEY ("assignee_id") REFERENCES "members" ("id")
);
