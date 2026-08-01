# rasql

`rasql` (pronounced ras-cal)` is an all-in-one SQL tool for Go.

It aims to support:

* Supports Pg/MySQL/SQLite/Spanner dialects
* Schema definition as Go code
  * Schema generation from live-database metadata
* Type-safe access to resultset values
* Dynamic query building at runtime
* Static query building using templates

Iternals:

* Go 1.26+
* Extensive use of parameterized types for maximum efficiency
