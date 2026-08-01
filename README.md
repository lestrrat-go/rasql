# rasql

`rasql` (pronounced “rascal”) is an all-in-one SQL tool for Go.

It aims to provide:

* PostgreSQL, MySQL, SQLite, and Google Cloud Spanner dialects.
* Schema definitions written as Go code, including generation from live database metadata.
* Type-safe result-set access.
* Dynamic query building at runtime.
* Static query building with templates.

The project requires Go 1.26 or newer and uses parameterized types where they improve type safety or avoid conversions.

See [DESIGN.md](DESIGN.md) for the architecture, boundaries, and planned implementation slices.
