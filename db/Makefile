GOOSE_DRIVER ?= postgres
DEV_URL  ?= postgres://postgres:postgres@localhost:5436/arti_dev?sslmode=disable
TEST_URL ?= postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable

.PHONY: migrate migrate-test reset reset-test dump-schema psql psql-test

migrate:
	goose -dir migrations $(GOOSE_DRIVER) "$(DEV_URL)" up

migrate-test:
	goose -dir migrations $(GOOSE_DRIVER) "$(TEST_URL)" up

reset:
	goose -dir migrations $(GOOSE_DRIVER) "$(DEV_URL)" reset

reset-test:
	goose -dir migrations $(GOOSE_DRIVER) "$(TEST_URL)" reset

dump-schema:
	pg_dump --schema-only --no-owner --no-privileges "$(DEV_URL)" > schema.sql

psql:
	psql "$(DEV_URL)"

psql-test:
	psql "$(TEST_URL)"
