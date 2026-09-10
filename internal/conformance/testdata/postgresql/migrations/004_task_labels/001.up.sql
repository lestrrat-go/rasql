CREATE TABLE task_labels (task_id INTEGER NOT NULL, label VARCHAR(32) NOT NULL, PRIMARY KEY (task_id,label), FOREIGN KEY (task_id) REFERENCES tasks(id));
