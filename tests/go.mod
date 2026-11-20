module github.com/npavlov/expense-platform/tests

go 1.26.11

replace github.com/npavlov/expense-platform/libs/platform => ../libs/platform

require (
	github.com/google/uuid v1.6.0
	github.com/npavlov/expense-platform/libs/platform v0.0.0-00010101000000-000000000000
	go.yaml.in/yaml/v3 v3.0.6
)

require github.com/go-jose/go-jose/v4 v4.1.9 // indirect
