CREATE TABLE "members" (
  "id" INTEGER NOT NULL,
  "name" TEXT NOT NULL,
  "email" TEXT NOT NULL,
  PRIMARY KEY ("id"),
  UNIQUE ("email")
);
