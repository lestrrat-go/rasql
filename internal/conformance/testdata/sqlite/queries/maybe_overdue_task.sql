SELECT id, project_id, assignee_id, title, is_open, due_on, created_at
FROM tasks
WHERE project_id = {{bind "projectID" tasks.project_id}}
  AND is_open = {{bind "open" tasks.is_open}}
  AND due_on IS NOT NULL
  AND due_on < {{bind "cutoff" tasks.due_on}}
ORDER BY id
