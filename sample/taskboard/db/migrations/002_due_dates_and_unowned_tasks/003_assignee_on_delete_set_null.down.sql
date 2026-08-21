ALTER TABLE "tasks"
  DROP CONSTRAINT "tasks_assignee_id_fkey",
  ADD CONSTRAINT "tasks_assignee_id_fkey"
    FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION;
