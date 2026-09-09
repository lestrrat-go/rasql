SELECT id, name FROM members WHERE id = ? ORDER BY id;
SELECT p.id, t.id, m.name FROM projects p LEFT JOIN tasks t ON t.project_id = p.id LEFT JOIN members m ON m.id = t.assignee_id ORDER BY p.id, t.id;
