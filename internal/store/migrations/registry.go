package migrations

type Definition struct {
	Version int
	Name    string
	Steps   []Step
}

type Step struct {
	Description string
	Build       func(usePostgres bool) (string, []any, error)
}

func Raw(description, query string) Step {
	return Step{
		Description: description,
		Build: func(bool) (string, []any, error) {
			return query, nil, nil
		},
	}
}

func RawDialect(description, sqliteQuery, postgresQuery string) Step {
	return Step{
		Description: description,
		Build: func(usePostgres bool) (string, []any, error) {
			if usePostgres {
				return postgresQuery, nil, nil
			}
			return sqliteQuery, nil, nil
		},
	}
}

func All() []Definition {
	return []Definition{
		migration001(),
		migration002(),
		migration003(),
		migration004(),
	}
}
