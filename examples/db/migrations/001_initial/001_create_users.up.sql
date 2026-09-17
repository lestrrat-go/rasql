CREATE TABLE "users" (
	"id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
	"email" TEXT NOT NULL,
	"nickname" TEXT,
	"status" TEXT NOT NULL DEFAULT 'pending',
	"first_name" TEXT NOT NULL,
	"last_name" TEXT NOT NULL
);
