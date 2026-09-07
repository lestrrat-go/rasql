SELECT u.id AS user_id, p.nickname AS nickname, count(p.user_id) AS profile_count
FROM users AS u
LEFT JOIN profiles AS p ON p.user_id = u.id
GROUP BY u.id, p.nickname
ORDER BY u.id
