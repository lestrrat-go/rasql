CREATE TABLE "projects" (
  "id" INTEGER NOT NULL,
  "owner_id" INTEGER NOT NULL,
  "name" TEXT NOT NULL,
  "archived" BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("owner_id") REFERENCES "members" ("id")
);
