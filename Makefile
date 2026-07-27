# BridgeXtra dev-stack shortcuts. See docker-compose.dev.yml for the seeded logins.
COMPOSE := docker compose -f docker-compose.dev.yml

.PHONY: dev dev-up dev-down dev-clean dev-seed dev-logs

## dev: bring up the full seeded stack in the foreground (Ctrl-C to stop)
dev:
	$(COMPOSE) up

## dev-up: bring up the full seeded stack detached
dev-up:
	$(COMPOSE) up -d

## dev-down: stop the stack (keeps the DB volume)
dev-down:
	$(COMPOSE) down

## dev-clean: stop the stack and wipe the DB + caches (fresh next time)
dev-clean:
	$(COMPOSE) down -v

## dev-seed: re-run the seeder against the running DB
dev-seed:
	$(COMPOSE) run --rm seed

## dev-logs: tail all service logs
dev-logs:
	$(COMPOSE) logs -f
