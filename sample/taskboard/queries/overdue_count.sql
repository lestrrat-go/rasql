SELECT COUNT(*) AS overdue
FROM tasks
WHERE is_open AND due_on IS NOT NULL AND due_on < CAST({{bind "on"}} AS date)
