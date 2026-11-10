#!/usr/bin/env bash
# Ensure the public Alt8Mail routes are proxied by the active nginx site.
#
# Application updates do not overwrite an administrator's nginx site. This
# script adds a small include to the TLS server block only when a required
# proxy location is missing, validates the result with nginx -t and restores
# the previous files if validation fails.
set -Eeuo pipefail

SITE="${A8M_NGINX_SITE:-/etc/nginx/sites-available/a8m}"
SNIPPET="${A8M_NGINX_SNIPPET:-/etc/nginx/snippets/a8m-public-routes.conf}"
UPSTREAM="${A8M_NGINX_UPSTREAM:-127.0.0.1:8080}"
ROUTES=(/files/s/ /upload/ /calendar/rsvp/ /calendar/poll/ /calendar/feed/ /oauth/ /scim/)
# Exact locations cover advertised base URLs that have no trailing slash.
EXACT_ROUTES=(/scim/v2)

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

[[ -f "$SITE" ]] || { log "nginx site $SITE not found; skipping public route check"; exit 0; }
command -v nginx >/dev/null 2>&1 || { log "nginx is not installed; skipping public route check"; exit 0; }

site_content="$(cat "$SITE")"
snippet_content=""
[[ -f "$SNIPPET" ]] && snippet_content="$(cat "$SNIPPET")"

missing=()
snippet_routes=()
snippet_exact_routes=()
for path in "${ROUTES[@]}"; do
  escaped_path="${path//\//\\/}"
  if grep -Eq "location[[:space:]]+[^\{]*[[:space:]]${escaped_path}" <<< "$site_content"; then
    continue
  fi
  snippet_routes+=("$path")
  if ! grep -Eq "location[[:space:]]+[^\{]*[[:space:]]${escaped_path}" <<< "$snippet_content"; then
    missing+=("$path")
  fi
done
for path in "${EXACT_ROUTES[@]}"; do
  escaped_path="${path//\//\\/}"
  exact_pattern="location[[:space:]]*=[[:space:]]*${escaped_path}[[:space:]]*\{"
  if grep -Eq "$exact_pattern" <<< "$site_content"; then
    continue
  fi
  snippet_exact_routes+=("$path")
  if ! grep -Eq "$exact_pattern" <<< "$snippet_content"; then
    missing+=("$path")
  fi
done
if [[ ${#missing[@]} -eq 0 ]]; then
  exit 0
fi

log "adding missing public nginx routes: ${missing[*]}"
install -d -m 0755 "$(dirname "$SNIPPET")"

snippet_backup="$(mktemp)"
site_backup="$(mktemp)"
updated="$(mktemp)"
trap 'rm -f "$snippet_backup" "$site_backup" "$updated"' EXIT
if [[ -f "$SNIPPET" ]]; then
  cp -a "$SNIPPET" "$snippet_backup"
else
  : > "$snippet_backup"
fi

# Preserve routes already supplied by the snippet and add newly required ones.
# Routes defined directly in the site are skipped so nginx never sees duplicate
# location blocks.
{
  printf '# Managed by Alt8Mail. Public download, upload, calendar, OAuth callback and SCIM routes.\n'
  for path in "${snippet_routes[@]}"; do
    body=""
    [[ "$path" == "/upload/" ]] && body='        client_max_body_size 12m;\n'
    printf 'location ^~ %s {\n        proxy_pass http://%s;\n        proxy_http_version 1.1;\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $remote_addr;\n        proxy_set_header X-Forwarded-Proto $scheme;\n%b    }\n' "$path" "$UPSTREAM" "$body"
  done
  for path in "${snippet_exact_routes[@]}"; do
    printf 'location = %s {\n        proxy_pass http://%s;\n        proxy_http_version 1.1;\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $remote_addr;\n        proxy_set_header X-Forwarded-Proto $scheme;\n        proxy_read_timeout 60s;\n        proxy_send_timeout 60s;\n    }\n' "$path" "$UPSTREAM"
  done
} > "$SNIPPET"
chmod 0644 "$SNIPPET"

cp -a "$SITE" "$site_backup"

if ! grep -Fq "include $SNIPPET;" "$SITE"; then
  if ! awk -v snippet="$SNIPPET" '
{
  line = $0
  if (!in_server && line ~ /^[[:space:]]*server[[:space:]]*\{/) {
    in_server = 1; depth = 0; tls = 0
  }
  if (in_server) {
    copy = line
    if (copy ~ /listen[^;]*443/) tls = 1
    opens = gsub(/\{/, "{", copy)
    closes = gsub(/\}/, "}", copy)
    if (closes > 0 && depth + opens - closes == 0) {
      if (tls && !inserted) {
        print "    include " snippet ";"
        inserted = 1
      }
      in_server = 0
    }
    depth += opens - closes
    print line
    next
  }
  print line
}
END { if (!inserted) exit 3 }
' "$site_backup" > "$updated"; then
    log "no TLS server block found in $SITE; previous public route snippet restored"
    if [[ -s "$snippet_backup" ]]; then cp -a "$snippet_backup" "$SNIPPET"; else rm -f "$SNIPPET"; fi
    exit 0
  fi
  cat "$updated" > "$SITE"
fi

if ! nginx -t >/dev/null 2>&1; then
  cp -a "$site_backup" "$SITE"
  if [[ -s "$snippet_backup" ]]; then cp -a "$snippet_backup" "$SNIPPET"; else rm -f "$SNIPPET"; fi
  nginx -t >/dev/null 2>&1 || true
  log "nginx rejected the public route include; the previous configuration was restored"
  exit 1
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl reload nginx || systemctl restart nginx || true
else
  nginx -s reload || true
fi
log "public nginx routes installed and nginx reloaded"
