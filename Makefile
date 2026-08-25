# Деплой сайта golang-mentor.
#
# Как всё устроено на сервере:
#
#   /var/www/golang-mentor   — репозиторий, он же вебрут nginx.
#                              Статику (html, reviews.js, картинки) обновляет
#                              сам git pull, копировать никуда не нужно.
#   /opt/my-backend/backend  — бинарник бэкенда, который запускает systemd.
#   backend.service          — юнит, работает от пользователя backend-user.
#   /opt/my-backend/.env     — SMTP_USER, SMTP_PASS и прочие переменные.
#
# То есть при деплое нужно: подтянуть код, собрать бинарник, положить его
# в /opt/my-backend и перезапустить сервис.
#
# Обычный сценарий — зайти на сервер по SSH и сделать:
#
#	cd /var/www/golang-mentor && make deploy
#
# Если что-то пошло не так: make info, make logs, make rollback.

SHELL := /bin/bash
.DEFAULT_GOAL := help

APP          ?= golang-mentor
BRANCH       ?= master
PORT         ?= 8080

# Куда собираем локально и куда потом кладём.
BUILD_BIN    ?= cmd/backend
DEPLOY_DIR   ?= /opt/my-backend
DEPLOY_BIN   ?= $(DEPLOY_DIR)/backend

# Из юнита backend.service.
SERVICE      ?= backend
SERVICE_USER ?= backend-user
ENV_FILE     ?= $(DEPLOY_DIR)/.env

SUDO         ?= sudo
GO           ?= go

# Курс, на котором проверяем живость API после перезапуска.
HEALTH_URL := http://127.0.0.1:$(PORT)/api/reviews?course=golang

.PHONY: help dev deploy deploy-force pull check build install-bin restart \
        health status logs info rollback show-service nginx-reload

## help: показать список команд
help:
	@echo "Деплой $(APP):"
	@echo
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  make /'
	@echo
	@echo "Репозиторий:  $(CURDIR)"
	@echo "Бинарник:     $(DEPLOY_BIN)"
	@echo "Сервис:       $(SERVICE).service (от $(SERVICE_USER))"
	@echo "Переменные:   $(ENV_FILE)"

## dev: поднять сайт локально на http://localhost:$(PORT)
dev:
	@if [ -f local.env ]; then \
		echo "Переменные из local.env — форма заявки будет слать письма по-настоящему."; \
		set -a; . ./local.env; set +a; ADDR=":$(PORT)" $(GO) run .; \
	else \
		echo "local.env не найден — поднимаю с DEV=1, форма заявки вернёт ошибку вместо письма."; \
		DEV=1 ADDR=":$(PORT)" $(GO) run .; \
	fi

## deploy: обновить код, пересобрать бинарник, разложить и перезапустить сервис
deploy: pull check build install-bin restart health
	@echo
	@echo "Готово: $$(git log -1 --format='%h %s')"

## deploy-force: то же самое, но со сбросом локальных правок на сервере
deploy-force:
	@echo ">> git reset --hard origin/$(BRANCH)"
	@git fetch origin $(BRANCH)
	@git reset --hard origin/$(BRANCH)
	@$(MAKE) --no-print-directory check build install-bin restart health
	@echo
	@echo "Готово: $$(git log -1 --format='%h %s')"

## pull: подтянуть свежий код (только fast-forward)
pull:
	@echo ">> git pull"
	@git pull --ff-only origin $(BRANCH) || { \
		echo; \
		echo "ОШИБКА: fast-forward не получился."; \
		echo "На сервере есть локальные правки или расхождение с origin/$(BRANCH)."; \
		echo "Глянь 'git status', либо снеси правки: make deploy-force"; \
		exit 1; \
	}

## check: gofmt, go vet и тесты (сеть не нужна)
check:
	@echo ">> проверки"
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "не отформатировано: $$out"; exit 1; fi
	@$(GO) vet ./...
	@$(GO) test ./...

## build: собрать бинарник в cmd/backend
build:
	@echo ">> сборка"
	@mkdir -p $$(dirname $(BUILD_BIN))
	@$(GO) build -trimpath -o $(BUILD_BIN) .
	@echo "   собран: $(BUILD_BIN) ($$(ls -lh $(BUILD_BIN) | awk '{print $$5}'))"

## install-bin: положить свежий бинарник в /opt/my-backend
install-bin:
	@echo ">> копирую в $(DEPLOY_BIN)"
	@if [ ! -f $(BUILD_BIN) ]; then echo "нет $(BUILD_BIN), сначала make build"; exit 1; fi
	@if [ -f $(DEPLOY_BIN) ]; then $(SUDO) cp -f $(DEPLOY_BIN) $(DEPLOY_BIN).prev; fi
	@# Пишем во временный файл рядом и переименовываем: перезаписать бинарник
	@# работающего процесса нельзя (text file busy), а rename — можно.
	@$(SUDO) install -o $(SERVICE_USER) -g $(SERVICE_USER) -m 755 $(BUILD_BIN) $(DEPLOY_BIN).new
	@$(SUDO) mv -f $(DEPLOY_BIN).new $(DEPLOY_BIN)
	@echo "   готово: $$($(SUDO) ls -l $(DEPLOY_BIN) | awk '{print $$3, $$5, $$6, $$7, $$8}')"

## restart: перезапустить сервис бэкенда
restart:
	@if ! systemctl list-unit-files 2>/dev/null | grep -q '^$(SERVICE)\.service'; then \
		echo "ОШИБКА: юнита $(SERVICE).service нет. Проверь: systemctl cat $(SERVICE)"; \
		exit 1; \
	fi
	@echo ">> systemctl restart $(SERVICE)"
	@$(SUDO) systemctl restart $(SERVICE)
	@sleep 1

## health: проверить, что бэкенд поднялся и отдаёт отзывы
health:
	@echo ">> проверка $(HEALTH_URL)"
	@for i in $$(seq 1 10); do \
		code=$$(curl -s -o /dev/null -w '%{http_code}' "$(HEALTH_URL)" || echo 000); \
		case "$$code" in \
		200) echo "   OK: бэкенд отвечает, отзывы отдаются"; exit 0 ;; \
		502) echo "   ВНИМАНИЕ: бэкенд живой, но не достучался до stepik.org."; \
		     echo "   Проверь исходящий доступ: curl -sI https://stepik.org/api/course-reviews"; \
		     exit 0 ;; \
		404) echo "   ОШИБКА: отвечает старый бинарник, маршрута /api/reviews нет."; \
		     echo "   Значит systemd поднял не тот файл. Смотри 'make info'."; \
		     exit 1 ;; \
		esac; \
		sleep 1; \
	done; \
	echo "   ОШИБКА: бэкенд не отвечает на порту $(PORT)"; \
	$(MAKE) --no-print-directory logs; \
	exit 1

## status: статус сервиса
status:
	@$(SUDO) systemctl status $(SERVICE) --no-pager -l | head -20

## logs: последние строки журнала сервиса
logs:
	@$(SUDO) journalctl -u $(SERVICE) -n 40 --no-pager

## info: что собрано, что разложено и что реально запущено
info:
	@echo "Репозиторий:      $(CURDIR)"
	@echo "Коммит:           $$(git log -1 --format='%h %s (%cr)' 2>/dev/null || echo '—')"
	@echo "Ветка:            $$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '—')"
	@echo
	@echo "Собранный:        $$(ls -l $(BUILD_BIN) 2>/dev/null | awk '{print $$5, $$6, $$7, $$8}' || echo 'не собран')"
	@echo "Разложенный:      $$($(SUDO) ls -l $(DEPLOY_BIN) 2>/dev/null | awk '{print $$3, $$5, $$6, $$7, $$8}' || echo 'нет')"
	@echo "Совпадают:        $$(if $(SUDO) cmp -s $(BUILD_BIN) $(DEPLOY_BIN) 2>/dev/null; then echo да; else echo 'НЕТ — нужен make install-bin'; fi)"
	@echo
	@st=$$(systemctl is-active $(SERVICE) 2>/dev/null || true); \
		echo "Сервис $(SERVICE):  $${st:-юнит не найден}"
	@echo
	@echo "Порт $(PORT):"
	@out=$$($(SUDO) ss -lptnH "sport = :$(PORT)" 2>/dev/null || true); \
		if [ -n "$$out" ]; then echo "$$out" | sed 's/^/  /'; else echo "  никто не слушает"; fi
	@echo
	@echo "Переменные $(ENV_FILE):"
	@if $(SUDO) test -f $(ENV_FILE); then echo "  есть, ключи: $$($(SUDO) grep -o '^[A-Z_]*' $(ENV_FILE) | tr '\n' ' ')"; else echo "  НЕТ — без SMTP_USER/SMTP_PASS сервис не стартует"; fi

## rollback: вернуть предыдущий бинарник и перезапустить
rollback:
	@if ! $(SUDO) test -f $(DEPLOY_BIN).prev; then echo "нет $(DEPLOY_BIN).prev, откатывать нечего"; exit 1; fi
	@echo ">> откат на предыдущий бинарник"
	@$(SUDO) cp -f $(DEPLOY_BIN) $(DEPLOY_BIN).broken
	@$(SUDO) mv -f $(DEPLOY_BIN).prev $(DEPLOY_BIN)
	@$(MAKE) --no-print-directory restart health

## show-service: показать текущий systemd-юнит
show-service:
	@$(SUDO) systemctl cat $(SERVICE)

## nginx-reload: проверить конфиг nginx и перечитать его
nginx-reload:
	@$(SUDO) nginx -t
	@$(SUDO) systemctl reload nginx
	@echo "   nginx перечитал конфиг"
