#!/bin/sh
# Installs (or migrates) microServer on a Linux server with Docker.
#
#   curl -fsSL https://raw.githubusercontent.com/vefghmhassan/microService/main/install.sh | sudo sh
#
# Re-running it on an existing checkout is safe: it keeps .env and data/.
# Variables: DIR (default /opt/microserver, or the current checkout when run
# from inside one), BRANCH (default main), REPO_URL.
set -eu

REPO_URL=${REPO_URL:-https://github.com/vefghmhassan/microService.git}
BRANCH=${BRANCH:-main}
if [ -z "${DIR:-}" ]; then
	if [ -f docker-compose.yml ] && [ -d .git ]; then DIR=$(pwd); else DIR=/opt/microserver; fi
fi

say() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root (sudo)"

if ! command -v git >/dev/null 2>&1; then
	say "installing git"
	if command -v apt-get >/dev/null; then apt-get update -qq && apt-get install -y -qq git
	elif command -v dnf >/dev/null; then dnf install -y git
	elif command -v yum >/dev/null; then yum install -y git
	elif command -v apk >/dev/null; then apk add --no-cache git
	else die "install git first"; fi
fi
if ! command -v docker >/dev/null 2>&1; then
	say "installing docker"
	curl -fsSL https://get.docker.com | sh
fi
docker compose version >/dev/null 2>&1 || die "docker compose plugin is missing"

if [ -d "$DIR/.git" ]; then
	say "using existing checkout $DIR"
else
	say "cloning $REPO_URL ($BRANCH) into $DIR"
	git clone -b "$BRANCH" "$REPO_URL" "$DIR"
fi
cd "$DIR"
DIR=$(pwd -P)

if [ ! -f .env ]; then
	cp .env.sample .env
	pw=$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | cut -c1-16)
	sed -i "s/^ADMIN_PASSWORD=.*/ADMIN_PASSWORD=$pw/" .env
	say "admin password: $pw  (saved in $DIR/.env)"
fi
# The updater mounts this directory at the same path.
if grep -q '^HOST_REPO_DIR=' .env; then
	sed -i "s#^HOST_REPO_DIR=.*#HOST_REPO_DIR=$DIR#" .env
else
	printf '\n# Absolute path of this checkout (used by the updater).\nHOST_REPO_DIR=%s\n' "$DIR" >> .env
fi
mkdir -p data

# An older install ran under the directory's project name; free its port.
old=$(docker ps -a --filter label=com.docker.compose.service=microserver --format '{{.ID}} {{.Label "com.docker.compose.project"}}' | awk '$2 != "microserver" {print $1}')
if [ -n "$old" ]; then
	say "removing the old container (data/ is kept)"
	docker rm -f $old >/dev/null
fi

COMMIT=$(git rev-parse HEAD)
BRANCH=$(git rev-parse --abbrev-ref HEAD)
VERSION=$(git describe --tags --always)
export COMMIT BRANCH VERSION
say "building and starting $VERSION ($BRANCH)"
docker compose up -d --build
# Keep a commit-tagged copy so the panel can switch back to this version.
docker tag microserver:current "microserver:$(echo "$COMMIT" | cut -c1-12)"

port=$(grep -E '^APP_PORT=' .env | cut -d= -f2)
say "done: http://$(hostname -I 2>/dev/null | awk '{print $1}'):${port:-8090}"
say "from now on update from the panel (منو: آپدیت پنل)"
