module github.com/lemmego/gpaorm

go 1.27

require (
	github.com/lemmego/gpa v0.1.1
	github.com/lemmego/orm v0.1.0
	github.com/mattn/go-sqlite3 v1.14.28
)

require (
	github.com/gertd/go-pluralize v0.2.1 // indirect
	github.com/iancoleman/strcase v0.3.0 // indirect
)

replace github.com/lemmego/orm => ../orm

replace github.com/lemmego/gpa => ../gpa
