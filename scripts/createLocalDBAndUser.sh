#!/usr/bin/env bash
#
# Create a local PostgreSQL role and database, then write their connection
# settings to the project-root .env file.

set -euo pipefail

usage() {
    echo "Usage: $0 <app-name>" >&2
    echo "Example: sudo $0 go-pdf-forge" >&2
}

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

if [[ $# -ne 1 ]]; then
    usage
    exit 1
fi

if [[ "${EUID}" -ne 0 ]]; then
    fail "run this script as root (for example: sudo $0 $1)"
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${PROJECT_ROOT}/.env"
if [[ -e "$ENV_FILE" ]]; then
    fail "$ENV_FILE already exists; refusing to overwrite it"
fi

command -v openssl >/dev/null || fail "openssl is required"
command -v psql >/dev/null || fail "psql is required"
command -v createdb >/dev/null || fail "createdb is required"
command -v runuser >/dev/null || fail "runuser is required"

APP_NAME="$1"
DB_NAME="$(
    printf '%s' "$APP_NAME" |
        sed --regexp-extended \
            --expression 's/([a-z0-9])([A-Z])/\1_\2/g' \
            --expression 's/[- ]/_/g' |
        tr '[:upper:]' '[:lower:]'
)"

if [[ ! "$DB_NAME" =~ ^[a-z][a-z0-9_]*$ ]]; then
    fail "app name must produce a PostgreSQL identifier matching ^[a-z][a-z0-9_]*$"
fi

run_as_postgres() {
    runuser --user postgres -- "$@"
}

postgres_value_exists() {
    local query="$1"
    [[ "$(run_as_postgres psql --dbname=postgres --tuples-only --no-align --set=ON_ERROR_STOP=1 --command "$query")" == "1" ]]
}

if postgres_value_exists "SELECT 1 FROM pg_roles WHERE rolname = '${DB_NAME}'"; then
    fail "PostgreSQL role ${DB_NAME} already exists"
fi

if postgres_value_exists "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'"; then
    fail "PostgreSQL database ${DB_NAME} already exists"
fi

# Hex avoids quoting issues in SQL and dotenv files while retaining strong entropy.
DB_PASSWORD="$(openssl rand -hex 24)"

umask 077
if ! (set -o noclobber; cat >"$ENV_FILE") <<EOF
DB_DRIVER=postgres
DB_HOST=127.0.0.1
DB_PORT=5432
DB_NAME=${DB_NAME}
DB_USER=${DB_NAME}
DB_PASSWORD=${DB_PASSWORD}
DB_SSL_MODE=prefer
DATABASE_URL=postgres://${DB_NAME}:${DB_PASSWORD}@127.0.0.1:5432/${DB_NAME}?sslmode=prefer
EOF
then
    fail "$ENV_FILE appeared while preparing the database; refusing to overwrite it"
fi

cleanup_env_on_failure() {
    rm -f -- "$ENV_FILE"
}
trap cleanup_env_on_failure EXIT

echo "Creating PostgreSQL role ${DB_NAME}"
printf 'CREATE USER "%s" WITH PASSWORD '\''%s'\'';\n' "$DB_NAME" "$DB_PASSWORD" |
    run_as_postgres psql --dbname=postgres --set=ON_ERROR_STOP=1

echo "Creating PostgreSQL database ${DB_NAME} owned by ${DB_NAME}"
if ! run_as_postgres createdb --owner="$DB_NAME" "$DB_NAME"; then
    echo "Database creation failed; removing the newly created role ${DB_NAME}" >&2
    printf 'DROP USER "%s";\n' "$DB_NAME" |
        run_as_postgres psql --dbname=postgres --set=ON_ERROR_STOP=1
    exit 1
fi

trap - EXIT
echo "Created ${ENV_FILE} with permissions restricted by umask 077"
