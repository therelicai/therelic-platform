#!/usr/bin/env bash
#
# setup.sh — interactive bootstrap for The Relic Platform.
#
# Asks four questions and writes .env. After this runs, the operator
# only needs `docker compose up`. Re-running is safe: existing .env
# values are surfaced as defaults so the wizard can also act as a
# reconfiguration tool.
#
# Non-TTY (CI, scripts): set every var below in the environment and
# pass --non-interactive. The wizard skips prompts and writes .env
# from the env vars.

set -euo pipefail

# ---- helpers ---------------------------------------------------------

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

# Print a bold header line. No-op styling on dumb terminals.
hdr() {
  if [[ -t 1 ]]; then printf "\033[1m%s\033[0m\n" "$*"; else printf "%s\n" "$*"; fi
}

ask() {
  # ask "Question" default_value VAR_NAME
  #
  # Precedence in non-interactive mode: existing env var > default.
  # Precedence in interactive mode:    typed input > default.
  local prompt="$1" default="$2" __var="$3" reply
  if [[ "${NON_INTERACTIVE:-0}" == "1" ]]; then
    # If the env var is already non-empty, keep it. Otherwise default.
    local current="${!__var:-}"
    if [[ -n "$current" ]]; then
      printf -v "$__var" '%s' "$current"
    else
      printf -v "$__var" '%s' "$default"
    fi
    return
  fi
  if [[ -n "$default" ]]; then
    read -r -p "$prompt [$default]: " reply || true
  else
    read -r -p "$prompt: " reply || true
  fi
  printf -v "$__var" '%s' "${reply:-$default}"
}

choose() {
  # choose "Question" VAR_NAME default option1 option2 option3 ...
  local prompt="$1" __var="$2" default="$3"; shift 3
  local opts=("$@") i=1 reply
  if [[ "${NON_INTERACTIVE:-0}" == "1" ]]; then
    local current="${!__var:-}"
    if [[ -n "$current" ]]; then
      printf -v "$__var" '%s' "$current"
    else
      printf -v "$__var" '%s' "$default"
    fi
    return
  fi
  echo
  hdr "$prompt"
  for o in "${opts[@]}"; do
    if [[ "$o" == "$default" ]]; then
      printf "  %d) %s  (default)\n" "$i" "$o"
    else
      printf "  %d) %s\n" "$i" "$o"
    fi
    ((i++))
  done
  read -r -p "Pick 1-${#opts[@]} [${default}]: " reply || true
  if [[ -z "$reply" ]]; then
    printf -v "$__var" '%s' "$default"
  elif [[ "$reply" =~ ^[0-9]+$ ]] && (( reply >= 1 && reply <= ${#opts[@]} )); then
    printf -v "$__var" '%s' "${opts[$((reply - 1))]}"
  else
    # If they typed a string that matches an option, accept it.
    for o in "${opts[@]}"; do
      if [[ "$o" == "$reply" ]]; then
        printf -v "$__var" '%s' "$o"
        return
      fi
    done
    echo "Invalid pick, using default: $default"
    printf -v "$__var" '%s' "$default"
  fi
}

gen_secret() {
  # 32-byte hex secret. openssl is on every dev machine we target;
  # fall back to /dev/urandom for the truly bare image.
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    head -c 32 /dev/urandom | xxd -p -c 64
  fi
}

# Read existing .env value to pre-populate the prompt on re-run.
existing() {
  local key="$1"
  [[ -f "$ENV_FILE" ]] || return
  awk -F= -v k="$key" '$1==k {sub(/^[^=]*=/,""); print; exit}' "$ENV_FILE" 2>/dev/null
}

# ---- non-interactive guard ------------------------------------------

if [[ ! -t 0 && "${NON_INTERACTIVE:-0}" != "1" ]]; then
  echo "stdin is not a terminal."
  echo "For automated setup, set every config var and pass --non-interactive:"
  echo "  RELIC_AUTH_MODE=local RELIC_ADMIN_EMAIL=you@example.com ... $0 --non-interactive"
  exit 1
fi

for arg in "$@"; do
  case "$arg" in
    --non-interactive) NON_INTERACTIVE=1 ;;
    -h|--help)
      sed -n '/^# setup.sh/,/^$/p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
  esac
done

# ---- prompts ---------------------------------------------------------

hdr "The Relic Platform — setup wizard"
echo
echo "This wizard writes .env. After it finishes, run \`docker compose up\`."
echo "Re-running is safe; existing values become defaults."
echo

# 1. Database
choose \
  "Where does Postgres run?" \
  POSTGRES_HOST_KIND "local-docker" \
  "local-docker" \
  "managed-supabase-neon-rds-other" \
  "edit-env-manually"

case "$POSTGRES_HOST_KIND" in
  local-docker)
    # docker-compose's own Postgres container. Default pw is "relic"
    # which is fine for laptop dev, never for anything network-exposed.
    : "${POSTGRES_PASSWORD:=$(existing POSTGRES_PASSWORD)}"
    ask "Postgres password (in-Docker)" "${POSTGRES_PASSWORD:-relic}" POSTGRES_PASSWORD
    DATABASE_URL=""
    ;;
  managed-supabase-neon-rds-other)
    : "${DATABASE_URL:=$(existing DATABASE_URL)}"
    ask "Full DATABASE_URL (postgres://user:pass@host:port/db?sslmode=require)" "$DATABASE_URL" DATABASE_URL
    ;;
  edit-env-manually)
    echo "  Skipping. Edit DATABASE_URL in .env yourself before booting."
    DATABASE_URL=""
    ;;
esac

# 2. Blob storage
choose \
  "Where does blob storage live?" \
  S3_KIND "local-minio" \
  "local-minio" \
  "cloudflare-r2" \
  "aws-s3" \
  "edit-env-manually"

case "$S3_KIND" in
  local-minio)
    : "${MINIO_ROOT_PASSWORD:=$(existing MINIO_ROOT_PASSWORD)}"
    ask "MinIO root password" "${MINIO_ROOT_PASSWORD:-relicminio}" MINIO_ROOT_PASSWORD
    S3_ENDPOINT=""; S3_REGION=""; S3_ACCESS_KEY=""; S3_SECRET_KEY=""; S3_BUCKET=""
    ;;
  cloudflare-r2)
    : "${R2_ACCOUNT:=}"
    ask "R2 account ID (the subdomain in your R2 endpoint)" "" R2_ACCOUNT
    S3_ENDPOINT="https://${R2_ACCOUNT}.r2.cloudflarestorage.com"
    S3_REGION="auto"
    : "${S3_ACCESS_KEY:=$(existing S3_ACCESS_KEY)}"
    : "${S3_SECRET_KEY:=$(existing S3_SECRET_KEY)}"
    : "${S3_BUCKET:=$(existing S3_BUCKET)}"
    ask "R2 access key ID" "$S3_ACCESS_KEY" S3_ACCESS_KEY
    ask "R2 secret access key" "$S3_SECRET_KEY" S3_SECRET_KEY
    ask "R2 bucket name" "${S3_BUCKET:-relic-traces}" S3_BUCKET
    ;;
  aws-s3)
    S3_ENDPOINT=""
    : "${S3_REGION:=$(existing S3_REGION)}"
    : "${S3_ACCESS_KEY:=$(existing S3_ACCESS_KEY)}"
    : "${S3_SECRET_KEY:=$(existing S3_SECRET_KEY)}"
    : "${S3_BUCKET:=$(existing S3_BUCKET)}"
    ask "AWS region" "${S3_REGION:-us-east-1}" S3_REGION
    ask "AWS access key ID" "$S3_ACCESS_KEY" S3_ACCESS_KEY
    ask "AWS secret access key" "$S3_SECRET_KEY" S3_SECRET_KEY
    ask "S3 bucket name" "${S3_BUCKET:-relic-traces}" S3_BUCKET
    ;;
  edit-env-manually)
    echo "  Skipping. Edit S3_* in .env yourself before booting."
    S3_ENDPOINT=""; S3_REGION=""; S3_ACCESS_KEY=""; S3_SECRET_KEY=""; S3_BUCKET=""
    ;;
esac

# 3. Auth mode
choose \
  "How will users sign in?" \
  RELIC_AUTH_MODE "local" \
  "local" \
  "supabase"

if [[ "$RELIC_AUTH_MODE" == "local" ]]; then
  : "${RELIC_JWT_SECRET:=$(existing RELIC_JWT_SECRET)}"
  if [[ -z "$RELIC_JWT_SECRET" ]]; then
    RELIC_JWT_SECRET="$(gen_secret)"
    echo "  Generated RELIC_JWT_SECRET."
  fi
  : "${RELIC_ADMIN_EMAIL:=$(existing RELIC_ADMIN_EMAIL)}"
  ask "First admin email" "$RELIC_ADMIN_EMAIL" RELIC_ADMIN_EMAIL
  : "${RELIC_ADMIN_PASSWORD:=}"
  ask "First admin password (>= 8 chars)" "$RELIC_ADMIN_PASSWORD" RELIC_ADMIN_PASSWORD
  if [[ ${#RELIC_ADMIN_PASSWORD} -lt 8 ]]; then
    echo "  Warning: password under 8 chars. The platform will reject it."
  fi
  SUPABASE_JWT_SECRET=""
else
  : "${SUPABASE_JWT_SECRET:=$(existing SUPABASE_JWT_SECRET)}"
  ask "SUPABASE_JWT_SECRET (from Supabase project settings)" "$SUPABASE_JWT_SECRET" SUPABASE_JWT_SECRET
  RELIC_JWT_SECRET=""; RELIC_ADMIN_EMAIL=""; RELIC_ADMIN_PASSWORD=""
fi

# 4. Trace integrity (security-sensitive but optional for v1).
: "${RELIC_TRACE_KEY:=$(existing RELIC_TRACE_KEY)}"
if [[ -z "$RELIC_TRACE_KEY" ]]; then
  RELIC_TRACE_KEY="$(gen_secret)"
  echo "  Generated RELIC_TRACE_KEY for HMAC chain verification."
fi
: "${RELIC_API_KEY_PEPPER:=$(existing RELIC_API_KEY_PEPPER)}"
if [[ -z "$RELIC_API_KEY_PEPPER" ]]; then
  RELIC_API_KEY_PEPPER="$(gen_secret)"
  echo "  Generated RELIC_API_KEY_PEPPER for API key hashing."
fi

# ---- write .env ------------------------------------------------------

if [[ -f "$ENV_FILE" ]]; then
  cp "$ENV_FILE" "$ENV_FILE.bak.$(date +%Y%m%d-%H%M%S)"
  echo
  echo "Existing .env backed up to $ENV_FILE.bak.*"
fi

{
  echo "# Generated by scripts/setup.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# Re-run scripts/setup.sh to reconfigure."
  echo
  echo "# --- Auth ---"
  echo "RELIC_AUTH_MODE=$RELIC_AUTH_MODE"
  [[ -n "$RELIC_JWT_SECRET" ]]     && echo "RELIC_JWT_SECRET=$RELIC_JWT_SECRET"
  [[ -n "$RELIC_ADMIN_EMAIL" ]]    && echo "RELIC_ADMIN_EMAIL=$RELIC_ADMIN_EMAIL"
  [[ -n "$RELIC_ADMIN_PASSWORD" ]] && echo "RELIC_ADMIN_PASSWORD=$RELIC_ADMIN_PASSWORD"
  [[ -n "$SUPABASE_JWT_SECRET" ]]  && echo "SUPABASE_JWT_SECRET=$SUPABASE_JWT_SECRET"
  echo
  echo "# --- Postgres ---"
  if [[ "$POSTGRES_HOST_KIND" == "local-docker" ]]; then
    echo "POSTGRES_PASSWORD=$POSTGRES_PASSWORD"
  elif [[ -n "$DATABASE_URL" ]]; then
    echo "DATABASE_URL=$DATABASE_URL"
  fi
  echo
  echo "# --- Blob storage ---"
  case "$S3_KIND" in
    local-minio)
      echo "MINIO_ROOT_PASSWORD=$MINIO_ROOT_PASSWORD"
      ;;
    cloudflare-r2|aws-s3)
      [[ -n "$S3_ENDPOINT" ]]   && echo "S3_ENDPOINT=$S3_ENDPOINT"
      [[ -n "$S3_REGION" ]]     && echo "S3_REGION=$S3_REGION"
      [[ -n "$S3_ACCESS_KEY" ]] && echo "S3_ACCESS_KEY=$S3_ACCESS_KEY"
      [[ -n "$S3_SECRET_KEY" ]] && echo "S3_SECRET_KEY=$S3_SECRET_KEY"
      [[ -n "$S3_BUCKET" ]]     && echo "S3_BUCKET=$S3_BUCKET"
      ;;
  esac
  echo
  echo "# --- Trace integrity (auto-generated; don't lose these) ---"
  echo "RELIC_TRACE_KEY=$RELIC_TRACE_KEY"
  echo "RELIC_API_KEY_PEPPER=$RELIC_API_KEY_PEPPER"
} > "$ENV_FILE"

chmod 600 "$ENV_FILE"

# ---- next step -------------------------------------------------------

echo
hdr "Setup complete."
echo
echo "Configuration written to:"
echo "  $ENV_FILE"
echo
echo "Next:"
echo "  docker compose up -d"
echo "  curl http://localhost:8080/readyz"
echo
if [[ "$RELIC_AUTH_MODE" == "local" ]]; then
  echo "Then open the dashboard. Log in with:"
  echo "  email:    $RELIC_ADMIN_EMAIL"
  echo "  password: (the one you set above)"
  echo
  echo "Build the dashboard pointed at this platform:"
  echo "  cd ../therelic-app"
  echo "  VITE_AUTH_MODE=local VITE_API_URL=http://localhost:8080/v1 npm run build"
  echo "  npx vite preview"
fi
