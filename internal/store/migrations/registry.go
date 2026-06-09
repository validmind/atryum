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
		migration005(),
		migration006(),
		migration007(),
		migration008(),
		migration009(),
		migration010(),
		migration011(),
		migration012(),
		migration013(),
		migration014(),
		migration015(),
		migration016(),
		migration017(),
		migration018(),
		migration019(),
	}
}
